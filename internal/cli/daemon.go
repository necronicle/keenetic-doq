package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const initScript = "/opt/etc/init.d/S56doqd"

// restartDaemon перезапускает демона через init-скрипт Entware и ждёт
// подтверждения alive. Вне роутера (скрипта нет) — предупреждение, nil.
func restartDaemon() error {
	if _, err := os.Stat(initScript); err != nil {
		fmt.Println("restart skipped: init script not found (not on the router?)")
		return nil
	}
	fmt.Print("restarting the daemon ... ")
	if out, err := exec.Command(initScript, "restart").CombinedOutput(); err != nil {
		fmt.Println("FAIL")
		return fmt.Errorf("restart failed: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	out, err := exec.Command(initScript, "check").CombinedOutput()
	if err == nil && strings.Contains(string(out), "alive") {
		fmt.Println("alive")
		return nil
	}
	fmt.Println("FAIL")
	return fmt.Errorf("daemon did not come back up (%s check: %s)", initScript, strings.TrimSpace(string(out)))
}

// daemonPID ищет работающий doqd по /proc/*/comm, пропуская себя.
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
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == "doqd" {
			return pid
		}
	}
	return 0
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
