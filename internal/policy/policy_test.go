package policy

import "testing"

func TestCommonPolicy(t *testing.T) {
	tests := []struct {
		text    string
		allowed bool
	}{
		{`ps`, true}, {`cat /etc/os-release`, true},
		{`ip -brief address`, true}, {`dmesg | tail -n 20`, true},
		{`echo x > /tmp/x`, false}, {`cat $(touch /tmp/pwn)`, false},
		{`sh -c 'id'`, false}, {`find / -exec rm {} \;`, false},
		{`unknown-vendor status`, false}, {`cat /etc/shadow`, false},
	}
	p := Common()
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := p.Evaluate(tt.text); got.Allowed != tt.allowed {
				t.Errorf("Evaluate(%q) = %+v, allowed=%v", tt.text, got, tt.allowed)
			}
		})
	}
}

func TestCommonPolicyAdversarialForms(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		allowed bool
	}{
		{"sequence", `uname -a; uptime && free || df`, true},
		{"quoted literal", `printf '%s\n' "fixed text"`, true},
		{"assignment", `LC_ALL=C ps`, false},
		{"background", `ps &`, false},
		{"negation", `! ps`, false},
		{"subshell", `(ps)`, false},
		{"function", `f() { ps; }; f`, false},
		{"conditional", `if true; then ps; fi`, false},
		{"arithmetic", `echo $((1+1))`, false},
		{"parameter expansion", `echo "$HOME"`, false},
		{"process environ", `cat /proc/1/../1/environ`, false},
		{"relative process environ", `cat proc/1/environ`, false},
		{"process memory", `head /proc/self/mem`, false},
		{"kernel memory", `cat /proc/kcore`, false},
		{"arbitrary process status", `cat /proc/123/status`, false},
		{"arbitrary proc node", `cat /proc/sys/kernel/random/boot_id`, false},
		{"arbitrary sys node", `cat /sys/kernel/uevent_seqnum`, false},
		{"null device", `cat /dev/null`, false},
		{"zero device", `head -c 1 /dev/zero`, false},
		{"self root shadow alias", `cat /proc/self/root/etc/shadow`, false},
		{"pid root shadow alias", `cat /proc/1/root/etc/shadow`, false},
		{"thread self root shadow alias", `cat /proc/thread-self/root/etc/shadow`, false},
		{"relative proc root shadow alias", `cat proc/123/root/etc/shadow`, false},
		{"proc root traversal alias", `cat /proc/self/root/../etc/shadow`, false},
		{"proc fd alias", `cat /proc/self/fd/3`, false},
		{"pid fd alias", `cat /proc/42/fd/7`, false},
		{"thread self fd alias", `cat /proc/thread-self/fd/9`, false},
		{"private key", `cat /root/.ssh/../.ssh/id_ed25519`, false},
		{"relative private key", `cat .ssh/id_rsa`, false},
		{"DSA host private key", `cat /etc/ssh/ssh_host_dsa_key`, false},
		{"legacy host private key", `cat /etc/ssh/ssh_host_key`, false},
		{"relative shadow", `cat ../../etc/shadow`, false},
		{"unsafe device", `cat /dev/mem`, false},
		{"gateway state", `cat /var/lib/ai-debug-gateway/audit.log`, false},
		{"find delete", `find /tmp -delete`, false},
		{"ip mutation", `ip link set eth0 down`, false},
		{"ip restore", `ip route restore`, false},
		{"ethtool mutation", `ethtool -s eth0 speed 100`, false},
		{"ethtool long mutation", `ethtool --set-eee eth0 eee off`, false},
		{"dmesg clear", `dmesg -c`, false},
		{"dmesg clear equals", `dmesg --console-level=3`, false},
		{"mount all", `mount -a`, false},
		{"hostname setter option", `hostname -F /tmp/name`, false},
		{"hostname long setter option", `hostname --file=/tmp/name`, false},
		{"hostname setter operand", `hostname new-name`, false},
		{"date setter option", `date -s tomorrow`, false},
		{"date busybox setter", `date 071512002026`, false},
		{"shell interpreter", `bash -c ps`, false},
		{"eval", `eval ps`, false},
		{"printenv name", `printenv PATH`, true},
		{"which tool", `which sh`, true},
		{"sed print", `sed -n '1,20p' /var/log/messages`, true},
		{"sed write", `sed -n '1w /tmp/pwn' /etc/os-release`, false},
	}
	p := Common()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Evaluate(tt.text); got.Allowed != tt.allowed {
				t.Errorf("Evaluate(%q) = %+v, allowed=%v", tt.text, got, tt.allowed)
			}
		})
	}
}

func TestCommonPolicyRejectsUnknownOptionsAndSecretOutput(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"ps environment modifier", `ps -eE`},
		{"ps unknown long option", `ps --everything`},
		{"free unknown option", `free --write-cache`},
		{"uptime unknown option", `uptime --all`},
		{"full environment", `printenv`},
		{"secret environment name", `printenv API_TOKEN`},
		{"password environment name", `printenv DB_PASSWORD`},
		{"cat unknown option", `cat --output=/tmp/x /etc/os-release`},
		{"head unknown option", `head --output=/tmp/x /etc/os-release`},
		{"wc path option", `wc --files0-from=/etc/shadow`},
		{"wc separated path option", `wc --files0-from /etc/shadow`},
		{"readlink unknown option", `readlink --read-secrets /etc/os-release`},
		{"ls unknown option", `ls --passwords`},
		{"stat unknown option", `stat --write /tmp/x`},
		{"du unknown option", `du --files0-from=/etc/shadow`},
		{"find unknown predicate", `find / -unknown-predicate value`},
		{"find printf action", `find / -printf '%p\n'`},
		{"find newer secret operand", `find / -newer /etc/shadow`},
		{"ethtool extra query operands", `ethtool -i eth0 unexpected`},
		{"type unknown option", `type --dump-all sh`},
	}
	p := Common()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Evaluate(tt.text); got.Allowed {
				t.Errorf("Evaluate(%q) = %+v, want denied", tt.text, got)
			}
		})
	}
}

func TestCommonPolicyAllowsExactDiagnosticForms(t *testing.T) {
	tests := []string{
		`ps -ef`, `free -m`, `uptime -p`, `df -h /`,
		`printenv PATH LANG`, `head -n 20 /var/log/messages`,
		`tail -c 128 /var/log/messages`, `wc -l /etc/os-release`,
		`cat /proc/cpuinfo`, `cat /proc/meminfo`, `cat /proc/uptime`,
		`head /proc/loadavg`, `cat /proc/version`, `cat /proc/filesystems`,
		`cat /proc/mounts`, `ls -la /tmp`,
		`stat -L /etc/os-release`, `du -sh /var/log`,
		`ethtool -i eth0`,
		`cat /home/operator/.ssh/id_ed25519.pub`,
		`cat /etc/ssh/ssh_host_dsa_key.pub`,
		`cat /etc/ssh/ssh_host_key.pub`,
		`type -a sh`,
		`ps --help`, `cat --version`, `ip --help`,
	}
	p := Common()
	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			if got := p.Evaluate(text); !got.Allowed {
				t.Errorf("Evaluate(%q) = %+v, want allowed", text, got)
			}
		})
	}
}
