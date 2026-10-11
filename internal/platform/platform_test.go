package platform

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  Kind
	}{
		// KeeneticOS: Entware в /opt, файла openwrt_release нет (проверено на
		// тест-роутере), хотя /etc/rc.common ссылается в /opt.
		{"keenetic", []string{"/opt/etc/init.d/rc.func", "/etc/rc.common"}, Keenetic},
		{"openwrt", []string{"/etc/openwrt_release", "/etc/rc.common"}, OpenWRT},
		{"unknown linux", nil, Keenetic},
	}
	for _, c := range cases {
		has := map[string]bool{}
		for _, f := range c.files {
			has[f] = true
		}
		got := detect(func(p string) bool { return has[p] })
		if got.Kind != c.want {
			t.Errorf("%s: detect = %v, want kind %v", c.name, got, c.want)
		}
	}
}

// Пути OpenWRT не должны вести в /opt (там нет Entware) и в /var (это tmpfs:
// закреплённый сервер терялся бы на каждой перезагрузке).
func TestOpenWRTPaths(t *testing.T) {
	p := openwrt
	for _, path := range []string{p.Conf, p.State, p.Init} {
		if len(path) >= 5 && (path[:5] == "/opt/" || path[:5] == "/var/" || path[:5] == "/tmp/") {
			t.Errorf("OpenWRT path %s is not on the persistent root filesystem", path)
		}
	}
	if keenetic.Conf != "/opt/etc/doqd.conf" || keenetic.State != "/opt/var/lib/doqd/geo.state" ||
		keenetic.Init != "/opt/etc/init.d/S56doqd" {
		t.Errorf("Keenetic paths changed: %+v — existing installs depend on them", keenetic)
	}
}
