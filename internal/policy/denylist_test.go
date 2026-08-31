package policy

import "testing"

func TestDenylistPolicy(t *testing.T) {
	p := Denylist(DenylistRules{DenyExecutables: []string{"mkfs.ext4"}})
	tests := []struct {
		text    string
		allowed bool
	}{
		{`mount -o remount,rw /`, true},
		{`echo hi > /tmp/x`, true},
		{`ls /nonexistent 2>&1 | tail -3`, true}, // fd duplication is a redirect, not a background job
		{`ls 2>&1 &`, false},
		{`ip link set eth0 down`, true},
		{`rm -rf /tmp/scratch`, true},
		{`chmod 600 /tmp/x`, true},
		{`a; b`, true}, // both unknown-but-permitted commands; classify allows unless denied
		{`sh -c 'id'`, false},
		{`bash -c 'id'`, false},
		{`busybox sh`, false},
		{"`ls`", false},
		{`cat $(touch /tmp/pwn)`, false},
		{`echo "$(id)"`, false},
		{`eval 'rm -rf /'`, false},
		{`exec rm -rf /`, false},
		{`env rm -rf /`, false},
		{`xargs rm`, false},
		{`find / -exec rm {} \;`, false},
		{`find / -okdir rm {} \;`, false},
		{`FOO=bar ls`, false},
		{`ls &`, false},
		{`ls; sh`, false}, // one denied simple command denies the whole line
		{`mkfs.ext4 /dev/mtd0`, false},
		{`cat /etc/shadow`, false},
		{`cat /proc/1/environ`, false},
		{`ls /sys/class/gpio`, false},
	}
	for _, tt := range tests {
		if got := p.Evaluate(tt.text); got.Allowed != tt.allowed {
			t.Errorf("Evaluate(%q) = %+v, want allowed=%v", tt.text, got, tt.allowed)
		}
	}
}

func TestDenylistCannotOverrideHardDenials(t *testing.T) {
	p := Denylist(DenylistRules{}) // no board rules at all
	for _, text := range []string{
		`sh -c 'id'`, `eval 'ls'`, "`ls`", `env ls`, `xargs ls`, `exec ls`,
		`find / -exec ls {} \;`, `python3 -c 'print(1)'`,
	} {
		if p.Evaluate(text).Allowed {
			t.Errorf("Evaluate(%q) must remain denied with no board rules", text)
		}
	}
}

func TestDenylistBoardRulesOnlyBlockWhatTheyName(t *testing.T) {
	p := Denylist(DenylistRules{
		DenyExecutables: []string{"reboot"},
		DenyExact:       []ArgvRule{{Executable: "dd", Args: []string{"if=/dev/zero", "of=/dev/mtd0"}}},
	})
	if p.Evaluate(`reboot`).Allowed {
		t.Fatal("denied executable must be denied")
	}
	if p.Evaluate(`poweroff`).Allowed != true {
		t.Fatal("unrelated executable must remain allowed")
	}
	if p.Evaluate(`dd if=/dev/zero of=/dev/mtd0`).Allowed {
		t.Fatal("exact denied invocation must be denied")
	}
	if !p.Evaluate(`dd if=/dev/zero of=/tmp/scratch`).Allowed {
		t.Fatal("a different invocation of the same executable must remain allowed")
	}
}

func TestCommonPolicyUnaffectedByDenylistExtraction(t *testing.T) {
	// Guards against the shellparse.go extraction changing Common()'s
	// existing, already-tested behavior.
	p := Common()
	tests := []struct {
		text    string
		allowed bool
	}{
		{`ps`, true}, {`cat /etc/os-release`, false}, {`cat /proc/cpuinfo`, true},
		{`ip -brief address`, true}, {`dmesg | tail -n 20`, true},
		{`echo x > /tmp/x`, false}, {`cat $(touch /tmp/pwn)`, false},
		{`sh -c 'id'`, false}, {`find / -exec rm {} \;`, false},
		{`unknown-vendor status`, false}, {`cat /etc/shadow`, false},
	}
	for _, tt := range tests {
		if got := p.Evaluate(tt.text); got.Allowed != tt.allowed {
			t.Errorf("Common Evaluate(%q) = %+v, want allowed=%v", tt.text, got, tt.allowed)
		}
	}
}

func TestDenylistAllowAllPathsBypassesSensitivePaths(t *testing.T) {
	p := Denylist(DenylistRules{AllowAllPaths: true})
	// Every one of these is denied by default (see the policy_test
	// sensitive-path table); with AllowAllPaths they must be allowed.
	for _, line := range []string{
		`cat /proc/1/environ`,
		`cat /proc/self/mem`,
		`cat /proc/kcore`,
		`cat /sys/class/net/eth0/address`,
		`cat /dev/mtd0`,
		`cat /etc/shadow`,
	} {
		if d := p.Evaluate(line); !d.Allowed {
			t.Errorf("AllowAllPaths should permit %q, denied: %s", line, d.Reason)
		}
	}
}

func TestDenylistAllowAllPathsStillEnforcesOtherRules(t *testing.T) {
	// AllowAllPaths only lifts the path check; hard-denied interpreters
	// and board deny rules must still bite.
	p := Denylist(DenylistRules{AllowAllPaths: true, DenyExecutables: []string{"reboot"}})
	for _, line := range []string{
		`sh -c 'cat /proc/1/environ'`, // interpreter hard-deny
		`reboot`,                      // board deny rule
	} {
		if d := p.Evaluate(line); d.Allowed {
			t.Errorf("AllowAllPaths must not permit %q", line)
		}
	}
}

func TestDenylistDefaultStillBlocksSensitivePaths(t *testing.T) {
	p := Denylist(DenylistRules{}) // AllowAllPaths defaults to false
	if d := p.Evaluate(`cat /proc/1/environ`); d.Allowed {
		t.Fatal("without AllowAllPaths, /proc/1/environ must stay denied")
	}
}
