// Package platform — на какой прошивке работает doqd и где у неё лежат его
// файлы. Один и тот же бинарник ставится и на Keenetic (Entware в /opt), и на
// OpenWRT (корневая файловая система), поэтому пути выбираются при запуске.
package platform

import "os"

type Kind int

const (
	Keenetic Kind = iota
	OpenWRT
)

// Platform — пути doqd на прошивке.
type Platform struct {
	Kind Kind
	// Conf — конфиг по умолчанию (флаг -c).
	Conf string
	// State — выбор сервера обхода; переживает рестарт, пишется редко.
	State string
	// Init — init-скрипт демона: им CLI предлагает запускать и перезапускать.
	Init string
}

func (p Platform) String() string {
	if p.Kind == OpenWRT {
		return "OpenWRT"
	}
	return "Keenetic"
}

var (
	keenetic = Platform{
		Kind:  Keenetic,
		Conf:  "/opt/etc/doqd.conf",
		State: "/opt/var/lib/doqd/geo.state",
		Init:  "/opt/etc/init.d/S56doqd",
	}
	// /var на OpenWRT — это tmpfs, поэтому состояние лежит в /etc: оно
	// пишется только при закреплении сервера и после оценки.
	openwrt = Platform{
		Kind:  OpenWRT,
		Conf:  "/etc/doqd.conf",
		State: "/etc/doqd/geo.state",
		Init:  "/etc/init.d/doqd",
	}
)

// openwrtRelease есть на каждой сборке OpenWRT (пакет base-files) и
// отсутствует на KeeneticOS, хотя та и собрана из OpenWRT buildroot.
const openwrtRelease = "/etc/openwrt_release"

// Detect определяет прошивку по файловой системе. Всё, что не OpenWRT,
// считается Keenetic: так doqd вёл себя до поддержки OpenWRT.
func Detect() Platform {
	return detect(func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func detect(exists func(string) bool) Platform {
	if exists(openwrtRelease) {
		return openwrt
	}
	return keenetic
}
