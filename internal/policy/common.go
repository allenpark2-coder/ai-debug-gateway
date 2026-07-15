package policy

import (
	"path"
	"strings"
)

func Common() *Policy {
	p := &Policy{commands: make(map[string]validator)}
	for _, command := range []string{"ps", "free", "uptime", "df", "findmnt", "ss", "netstat", "logread", "uname", "id", "whoami", "groups", "pwd"} {
		p.commands[command] = optionsOnly(command)
	}
	p.commands["printenv"] = pureCommand("printenv")
	p.commands["which"] = pureCommand("which")
	p.commands["mount"] = mountCommand
	p.commands["dmesg"] = dmesgCommand
	for _, command := range []string{"cat", "head", "tail", "wc", "readlink", "ls", "stat", "du"} {
		p.commands[command] = pathCommand(command)
	}
	p.commands["sed"] = sedCommand
	p.commands["tr"] = trCommand
	p.commands["top"] = topCommand
	p.commands["route"] = routeCommand
	p.commands["ip"] = ipCommand
	p.commands["ethtool"] = ethtoolCommand
	p.commands["hostname"] = hostnameCommand
	p.commands["date"] = dateCommand
	p.commands["find"] = findCommand
	p.commands["echo"] = literalOutput("echo")
	p.commands["printf"] = literalOutput("printf")
	p.commands["command"] = lookupCommand("command")
	p.commands["type"] = lookupCommand("type")
	return p
}

func pureCommand(command string) validator {
	return func([]string) Decision { return allow("command." + command) }
}

func optionsOnly(command string) validator {
	return func(argv []string) Decision {
		for _, arg := range argv[1:] {
			if !strings.HasPrefix(arg, "-") {
				return deny("command."+command, "positional operands are not allowed for this command")
			}
		}
		return allow("command." + command)
	}
}

func pathCommand(command string) validator {
	return func(argv []string) Decision {
		for _, arg := range argv[1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if reason := unsafePath(arg); reason != "" {
				return deny("path.sensitive", reason)
			}
		}
		return allow("command." + command)
	}
}

func unsafePath(name string) string {
	clean := path.Clean(name)
	lower := strings.ToLower(clean)
	relative := lower
	for strings.HasPrefix(relative, "../") {
		relative = strings.TrimPrefix(relative, "../")
	}
	relative = strings.TrimPrefix(strings.TrimPrefix(relative, "./"), "/")
	if contains([]string{"etc/shadow", "etc/gshadow", "etc/passwd-", "etc/shadow-"}, relative) {
		return "password database reads are not allowed"
	}
	base := path.Base(lower)
	if (strings.Contains("/"+relative, "/.ssh/") && (strings.HasPrefix(base, "id_") || base == "authorized_keys")) ||
		base == "ssh_host_rsa_key" || base == "ssh_host_ecdsa_key" || base == "ssh_host_ed25519_key" {
		return "SSH key material reads are not allowed"
	}
	if strings.HasPrefix(relative, "proc/") && (base == "environ" || base == "mem") {
		return "process secrets or memory reads are not allowed"
	}
	if strings.HasPrefix(lower, "/dev/") && clean != "/dev/null" {
		return "device-node reads are not allowed"
	}
	if strings.Contains(lower, "ai-debug-gateway") || strings.Contains(lower, "/gatewayd/") {
		return "gateway state reads are not allowed"
	}
	return ""
}

func sedCommand(argv []string) Decision {
	if len(argv) < 3 || argv[1] != "-n" {
		return deny("command.sed", "only sed -n is allowed")
	}
	if !safeSedPrintProgram(argv[2]) {
		return deny("command.sed", "only numeric-address print programs are allowed")
	}
	return pathCommand("sed")(append([]string{"sed"}, argv[3:]...))
}

func safeSedPrintProgram(program string) bool {
	if len(program) < 1 || program[len(program)-1] != 'p' {
		return false
	}
	for _, r := range strings.TrimSpace(program[:len(program)-1]) {
		if (r < '0' || r > '9') && r != ',' && r != '$' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func trCommand(argv []string) Decision {
	if len(argv) < 2 || len(argv) > 3 {
		return deny("command.tr", "tr requires one or two literal character sets")
	}
	return allow("command.tr")
}

func topCommand(argv []string) Decision {
	for _, arg := range argv[1:] {
		if arg == "-b" || arg == "-n" || isDecimal(arg) {
			continue
		}
		return deny("command.top", "top must use batch-mode options only")
	}
	if !contains(argv[1:], "-b") {
		return deny("command.top", "interactive top is not allowed")
	}
	return allow("command.top")
}

func routeCommand(argv []string) Decision {
	if len(argv) == 1 || len(argv) == 2 && argv[1] == "-n" {
		return allow("command.route")
	}
	return deny("command.route", "only route -n is allowed")
}

func ipCommand(argv []string) Decision {
	args := argv[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] != "-brief" && args[0] != "-details" && args[0] != "-statistics" && args[0] != "-json" && args[0] != "-oneline" && args[0] != "-4" && args[0] != "-6" {
			return deny("command.ip", "unknown ip option is not allowed")
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return deny("command.ip", "an ip object is required")
	}
	objects := []string{"address", "addr", "link", "route", "rule", "neighbour", "neighbor", "netns", "maddress", "tunnel"}
	if !contains(objects, args[0]) {
		return deny("command.ip", "ip object is not read-only")
	}
	if len(args) > 1 && !contains([]string{"show", "list", "get"}, args[1]) {
		return deny("command.ip", "only ip show, list, and get forms are allowed")
	}
	return allow("command.ip")
}

func ethtoolCommand(argv []string) Decision {
	if len(argv) < 2 {
		return deny("command.ethtool", "an interface or read-only query is required")
	}
	if !strings.HasPrefix(argv[1], "-") {
		if len(argv) == 2 {
			return allow("command.ethtool")
		}
		return deny("command.ethtool", "extra ethtool operands are not allowed")
	}
	readOptions := []string{"-i", "--driver", "-k", "--show-features", "-g", "--show-ring", "-c", "--show-coalesce", "-a", "--show-pause", "-S", "--statistics", "-T", "--show-time-stamping", "-P", "--show-permaddr", "-l", "--show-channels", "-m", "--dump-module-eeprom", "-e", "--eeprom-dump", "-d", "--register-dump"}
	if !contains(readOptions, argv[1]) || len(argv) < 3 {
		return deny("command.ethtool", "only explicit read-only ethtool queries are allowed")
	}
	return allow("command.ethtool")
}

func mountCommand(argv []string) Decision {
	if len(argv) == 1 || len(argv) == 2 && contains([]string{"-l", "--show-labels"}, argv[1]) {
		return allow("command.mount")
	}
	return deny("command.mount", "only listing mounted filesystems is allowed")
}

func dmesgCommand(argv []string) Decision {
	for _, arg := range argv[1:] {
		if !contains([]string{"-H", "--human", "-T", "--ctime", "-t", "--notime", "-x", "--decode", "-r", "--raw", "-k", "--kernel", "-u", "--userspace", "-w", "--follow"}, arg) {
			return deny("command.dmesg", "unknown or state-changing dmesg options are not allowed")
		}
	}
	return allow("command.dmesg")
}

func hostnameCommand(argv []string) Decision {
	if len(argv) == 1 || len(argv) == 2 && contains([]string{"-s", "--short", "-f", "--fqdn", "-d", "--domain", "-i", "--ip-address", "-a", "--alias"}, argv[1]) {
		return allow("command.hostname")
	}
	return deny("command.hostname", "setting the hostname is not allowed")
}

func dateCommand(argv []string) Decision {
	for _, arg := range argv[1:] {
		if strings.HasPrefix(arg, "+") || contains([]string{"-u", "--utc", "--universal", "-R", "--rfc-2822", "-I", "--iso-8601"}, arg) || strings.HasPrefix(arg, "--iso-8601=") {
			continue
		}
		return deny("command.date", "date setters and unknown date forms are not allowed")
	}
	return allow("command.date")
}

func findCommand(argv []string) Decision {
	mutating := []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls"}
	for _, arg := range argv[1:] {
		if contains(mutating, arg) {
			return deny("command.find", "find mutation or command-execution actions are not allowed")
		}
		if !strings.HasPrefix(arg, "-") {
			if reason := unsafePath(arg); reason != "" {
				return deny("path.sensitive", reason)
			}
		}
	}
	return allow("command.find")
}

func literalOutput(command string) validator {
	return func([]string) Decision { return allow("command." + command) }
}

func lookupCommand(command string) validator {
	return func(argv []string) Decision {
		if command == "command" && (len(argv) < 3 || argv[1] != "-v") || len(argv) < 2 {
			return deny("command."+command, "only executable lookup is allowed")
		}
		return allow("command." + command)
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
