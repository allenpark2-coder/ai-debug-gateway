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
		{"private key", `cat /root/.ssh/../.ssh/id_ed25519`, false},
		{"relative private key", `cat .ssh/id_rsa`, false},
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
