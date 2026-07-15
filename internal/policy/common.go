package policy

import (
	"path"
	"strings"
)

func Common() *Policy {
	p := &Policy{commands: make(map[string]validator)}
	p.commands["ps"] = exactOptions("ps", []string{"-A", "-a", "-e", "-f", "-l", "-T", "-w", "-x", "-ef"})
	p.commands["free"] = exactOptions("free", []string{"-b", "-k", "-m", "-g", "-h", "-w", "-t"})
	p.commands["uptime"] = exactOptions("uptime", []string{"-p", "--pretty", "-s", "--since", "-V", "--version"})
	p.commands["df"] = exactPathOptions("df", []string{"-a", "-h", "-H", "-i", "-k", "-l", "-P", "-T"})
	p.commands["findmnt"] = exactOptions("findmnt", []string{"-a", "--all", "-l", "--list", "-r", "--raw", "-n", "--noheadings"})
	p.commands["ss"] = exactOptions("ss", []string{"-a", "-l", "-n", "-t", "-u", "-x", "-4", "-6", "-s"})
	p.commands["netstat"] = exactOptions("netstat", []string{"-a", "-l", "-n", "-r", "-s", "-t", "-u", "-x", "-w"})
	p.commands["logread"] = exactOptions("logread", []string{"-f", "-F", "-r"})
	p.commands["uname"] = exactOptions("uname", []string{"-a", "-s", "-n", "-r", "-v", "-m", "-p", "-i", "-o"})
	p.commands["id"] = exactOptionsWithOperands("id", []string{"-a", "-g", "-G", "-n", "-r", "-u", "-Z"})
	p.commands["groups"] = exactOptionsWithOperands("groups", nil)
	p.commands["whoami"] = exactOptions("whoami", nil)
	p.commands["pwd"] = exactOptions("pwd", []string{"-L", "-P"})
	p.commands["printenv"] = printenvCommand
	p.commands["which"] = exactOptionsWithOperands("which", []string{"-a"})
	p.commands["mount"] = mountCommand
	p.commands["dmesg"] = dmesgCommand
	p.commands["cat"] = exactPathOptions("cat", []string{"-A", "-b", "-e", "-E", "-n", "-s", "-t", "-T", "-u", "-v"})
	p.commands["head"] = headTailCommand("head")
	p.commands["tail"] = headTailCommand("tail")
	p.commands["wc"] = exactPathOptions("wc", []string{"-c", "--bytes", "-l", "--lines", "-m", "--chars", "-w", "--words", "-L", "--max-line-length"})
	p.commands["readlink"] = exactPathOptions("readlink", []string{"-f", "--canonicalize", "-e", "--canonicalize-existing", "-m", "--canonicalize-missing", "-n", "--no-newline", "-q", "-s", "-v", "-z", "--zero"})
	p.commands["ls"] = exactPathOptions("ls", []string{"-a", "-A", "-d", "-F", "-h", "-i", "-l", "-la", "-al", "-lh", "-hl", "-lah", "-alh", "-n", "-r", "-R", "-s", "-t", "-u"})
	p.commands["stat"] = statCommand
	p.commands["du"] = duCommand
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
	for executable, validate := range p.commands {
		p.commands[executable] = withInformationOptions(executable, validate)
	}
	return p
}

func withInformationOptions(command string, validate validator) validator {
	return func(argv []string) Decision {
		if len(argv) == 2 && (argv[1] == "--help" || argv[1] == "--version") {
			return allow("command." + command + ".information")
		}
		return validate(argv)
	}
}

func exactOptions(command string, options []string) validator {
	return func(argv []string) Decision {
		for _, arg := range argv[1:] {
			if !contains(options, arg) {
				return deny("command."+command, "unknown option or positional operand is not allowed")
			}
		}
		return allow("command." + command)
	}
}

func exactOptionsWithOperands(command string, options []string) validator {
	return func(argv []string) Decision {
		for _, arg := range argv[1:] {
			if strings.HasPrefix(arg, "-") && !contains(options, arg) {
				return deny("command."+command, "unknown option is not allowed")
			}
		}
		return allow("command." + command)
	}
}

func exactPathOptions(command string, options []string) validator {
	return func(argv []string) Decision {
		endOptions := false
		for _, arg := range argv[1:] {
			if arg == "--" && !endOptions {
				endOptions = true
				continue
			}
			if !endOptions && strings.HasPrefix(arg, "-") {
				if !contains(options, arg) {
					return deny("command."+command, "unknown option is not allowed")
				}
				continue
			}
			if reason := unsafePath(arg); reason != "" {
				return deny("path.sensitive", reason)
			}
		}
		return allow("command." + command)
	}
}

func printenvCommand(argv []string) Decision {
	allowed := []string{"PATH", "LANG", "LC_ALL", "TERM", "SHELL", "USER", "LOGNAME", "HOSTNAME", "TZ"}
	if len(argv) < 2 {
		return deny("command.printenv", "dumping the complete environment is not allowed")
	}
	for _, name := range argv[1:] {
		if !contains(allowed, name) {
			return deny("command.printenv", "environment name is not on the safe allowlist")
		}
	}
	return allow("command.printenv")
}

func headTailCommand(command string) validator {
	return func(argv []string) Decision {
		for i := 1; i < len(argv); i++ {
			arg := argv[i]
			if arg == "--" {
				return validatePaths(command, argv[i+1:])
			}
			if arg == "-q" || arg == "--quiet" || arg == "-v" || arg == "--verbose" {
				continue
			}
			if arg == "-n" || arg == "--lines" || arg == "-c" || arg == "--bytes" {
				i++
				if i >= len(argv) || !isCount(argv[i]) {
					return deny("command."+command, "count option requires a numeric value")
				}
				continue
			}
			if (strings.HasPrefix(arg, "-n") || strings.HasPrefix(arg, "-c")) && len(arg) > 2 && isCount(arg[2:]) {
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return deny("command."+command, "unknown option is not allowed")
			}
			return validatePaths(command, argv[i:])
		}
		return allow("command." + command)
	}
}

func statCommand(argv []string) Decision {
	for i := 1; i < len(argv); i++ {
		if contains([]string{"-L", "--dereference", "-f", "--file-system", "-t", "--terse"}, argv[i]) {
			continue
		}
		if argv[i] == "-c" || argv[i] == "--format" || argv[i] == "--printf" {
			i++
			if i >= len(argv) {
				return deny("command.stat", "format option requires a value")
			}
			continue
		}
		if strings.HasPrefix(argv[i], "-") {
			return deny("command.stat", "unknown option is not allowed")
		}
		return validatePaths("stat", argv[i:])
	}
	return deny("command.stat", "a path operand is required")
}

func duCommand(argv []string) Decision {
	for i := 1; i < len(argv); i++ {
		if contains([]string{"-a", "-h", "-s", "-k", "-m", "-x", "-L", "-P", "-sh", "-hs"}, argv[i]) {
			continue
		}
		if argv[i] == "-d" || argv[i] == "--max-depth" {
			i++
			if i >= len(argv) || !isDecimal(argv[i]) {
				return deny("command.du", "max depth requires a decimal value")
			}
			continue
		}
		if strings.HasPrefix(argv[i], "--max-depth=") && isDecimal(strings.TrimPrefix(argv[i], "--max-depth=")) {
			continue
		}
		if strings.HasPrefix(argv[i], "-") {
			return deny("command.du", "unknown option is not allowed")
		}
		return validatePaths("du", argv[i:])
	}
	return allow("command.du")
}

func validatePaths(command string, paths []string) Decision {
	for _, name := range paths {
		if strings.HasPrefix(name, "-") {
			return deny("command."+command, "unknown option is not allowed")
		}
		if reason := unsafePath(name); reason != "" {
			return deny("path.sensitive", reason)
		}
	}
	return allow("command." + command)
}

func unsafePath(name string) string {
	clean := path.Clean(name)
	lower := strings.ToLower(clean)
	relative := lower
	for strings.HasPrefix(relative, "../") {
		relative = strings.TrimPrefix(relative, "../")
	}
	relative = strings.TrimPrefix(strings.TrimPrefix(relative, "./"), "/")
	if reason := unsafePseudoFilesystemPath(relative); reason != "" {
		return reason
	}
	if contains([]string{"etc/shadow", "etc/gshadow", "etc/passwd-", "etc/shadow-"}, relative) {
		return "password database reads are not allowed"
	}
	base := path.Base(lower)
	if (strings.Contains("/"+relative, "/.ssh/") &&
		((strings.HasPrefix(base, "id_") && !strings.HasSuffix(base, ".pub")) || base == "authorized_keys")) ||
		(strings.HasPrefix(base, "ssh_host_") && strings.HasSuffix(base, "_key")) {
		return "SSH key material reads are not allowed"
	}
	if strings.Contains(lower, "ai-debug-gateway") || strings.Contains(lower, "/gatewayd/") {
		return "gateway state reads are not allowed"
	}
	return ""
}

func unsafePseudoFilesystemPath(relative string) string {
	parts := strings.Split(relative, "/")
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "proc":
		allowed := []string{
			"proc/cpuinfo", "proc/meminfo", "proc/uptime", "proc/loadavg",
			"proc/version", "proc/filesystems", "proc/mounts",
		}
		if contains(allowed, relative) {
			return ""
		}
		return "procfs reads are limited to explicitly allowlisted global diagnostics"
	case "sys":
		return "sysfs reads are not allowlisted"
	case "dev":
		return "device-node reads are not allowed"
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
	return exactPathOptions("sed", nil)(append([]string{"sed"}, argv[3:]...))
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
	if !contains(readOptions, argv[1]) || len(argv) != 3 {
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
	args := argv[1:]
	i := 0
	for i < len(args) && contains([]string{"-H", "-L", "-P"}, args[i]) {
		i++
	}
	for i < len(args) && !strings.HasPrefix(args[i], "-") && args[i] != "!" && args[i] != "(" && args[i] != ")" {
		if reason := unsafePath(args[i]); reason != "" {
			return deny("path.sensitive", reason)
		}
		i++
	}
	for i < len(args) {
		arg := args[i]
		switch {
		case contains([]string{"!", "(", ")", "-not", "-a", "-and", "-o", "-or", "-print", "-print0", "-ls", "-prune", "-quit", "-empty", "-readable", "-xdev", "-mount"}, arg):
			i++
		case contains([]string{"-name", "-iname", "-path", "-ipath", "-type", "-user", "-group", "-size", "-mtime", "-mmin", "-maxdepth", "-mindepth", "-links", "-inum", "-perm"}, arg):
			if i+1 >= len(args) {
				return deny("command.find", "find test requires exactly one operand")
			}
			i += 2
		default:
			return deny("command.find", "unknown find option, predicate, or action is not allowed")
		}
	}
	return allow("command.find")
}

func literalOutput(command string) validator {
	return func([]string) Decision { return allow("command." + command) }
}

func lookupCommand(command string) validator {
	return func(argv []string) Decision {
		if command == "command" {
			if len(argv) < 3 || argv[1] != "-v" {
				return deny("command.command", "only command -v lookups are allowed")
			}
			return allow("command.command")
		}
		if len(argv) < 2 {
			return deny("command."+command, "only executable lookup is allowed")
		}
		for i, arg := range argv[1:] {
			if strings.HasPrefix(arg, "-") && (i > 0 || !contains([]string{"-a", "-f", "-p", "-P", "-t"}, arg)) {
				return deny("command.type", "unknown type option is not allowed")
			}
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

func isCount(value string) bool {
	value = strings.TrimPrefix(strings.TrimPrefix(value, "+"), "-")
	return isDecimal(value)
}
