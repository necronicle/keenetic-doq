package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const initScript = "/opt/etc/init.d/S56doqd"

// IsDaemonArgs отличает демона от процесса утилиты: у обоих одно и то же
// имя (doqd), но утилита всегда запускается с подкомандой в argv[1],
// а демон — либо без аргументов, либо только с флагами.
func IsDaemonArgs(args []string) bool {
	if len(args) < 2 {
		return true
	}
	return strings.HasPrefix(args[1], "-")
}

// procArgs читает argv процесса из /proc/<pid>/cmdline.
func procArgs(pid int) ([]string, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	return parts, true
}

// daemonPID ищет работающий демон doqd по /proc, пропуская себя и любые
// процессы утилиты (они называются так же).
func daemonPID() int {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0 // не Linux — статус демона недоступен
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		comm, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil || strings.TrimSpace(string(comm)) != "doqd" {
			continue
		}
		args, ok := procArgs(pid)
		if !ok || !IsDaemonArgs(args) {
			continue
		}
		return pid
	}
	return 0
}

// restartDaemon перезапускает демона тем же argv, которым он был запущен.
//
// Init-скрипт Entware для этого не годится: rc.func останавливает демона
// через `killall doqd`, а процесс утилиты называется так же и погибает
// вместе с ним, не успев запустить демона обратно.
func restartDaemon() error {
	pid := daemonPID()
	if pid == 0 {
		fmt.Printf("daemon is not running — config saved, start it with: %s start\n", initScript)
		return nil
	}
	args, ok := procArgs(pid)
	if !ok || len(args) == 0 {
		return fmt.Errorf("cannot read the command line of the running daemon (pid %d)", pid)
	}

	fmt.Print("restarting the daemon ... ")
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Println("FAIL")
		return fmt.Errorf("cannot stop the daemon (pid %d): %w", pid, err)
	}
	if !waitGone(pid, 10*time.Second) {
		fmt.Println("FAIL")
		return fmt.Errorf("daemon (pid %d) did not stop", pid)
	}

	// Setsid отвязывает нового демона от нашей сессии и терминала, иначе
	// он умрёт вместе с ssh-сессией, из которой запущена утилита.
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Println("FAIL")
		return err
	}
	defer devNull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	if err := cmd.Start(); err != nil {
		fmt.Println("FAIL")
		return fmt.Errorf("cannot start the daemon: %w", err)
	}
	// Родителем осиротевшего процесса станет init; Release, чтобы не
	// оставлять зомби на время жизни утилиты.
	if err := cmd.Process.Release(); err != nil {
		fmt.Println("FAIL")
		return err
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if newPID := daemonPID(); newPID > 0 {
			fmt.Printf("alive (pid %d)\n", newPID)
			return nil
		}
	}
	fmt.Println("FAIL")
	return fmt.Errorf("daemon did not come back up — check the logs and run: %s start", initScript)
}

// waitGone ждёт исчезновения процесса.
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); os.IsNotExist(err) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// daemonUptime считает аптайм процесса по starttime из /proc/<pid>/stat
// (поле 22) и /proc/uptime. CLK_TCK на Linux — 100.
func daemonUptime(pid int) (time.Duration, bool) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	i := bytes.LastIndexByte(stat, ')') // имя процесса в скобках может содержать пробелы
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 20 {
		return 0, false
	}
	ticks, err := strconv.ParseFloat(fields[19], 64)
	if err != nil {
		return 0, false
	}
	up, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	var sys float64
	if _, err := fmt.Sscanf(string(up), "%f", &sys); err != nil {
		return 0, false
	}
	const clkTck = 100
	return time.Duration(sys-ticks/clkTck) * time.Second, true
}

// ndmShow выполняет команду CLI KeeneticOS через ndmc (или старый ndmq).
func ndmShow(cmd string) (string, bool) {
	if p, err := exec.LookPath("ndmc"); err == nil {
		out, _ := exec.Command(p, "-c", cmd).CombinedOutput()
		return string(out), true
	}
	if p, err := exec.LookPath("ndmq"); err == nil {
		out, _ := exec.Command(p, "-p", cmd).CombinedOutput()
		return string(out), true
	}
	return "", false
}
