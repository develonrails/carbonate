package main

import "strings"

// Flags that consume the following argument as their value. reorderArgs needs
// this to avoid mistaking a flag's value for a positional argument.
var (
	authFlagsWithValues      = map[string]bool{"session": true}
	serveFlagsWithValues     = map[string]bool{"session": true, "addr": true}
	calendarsFlagsWithValues = map[string]bool{"session": true}
	eventFlagsWithValues     = map[string]bool{"session": true, "calendar": true, "uid": true}
)

// reorderArgs moves positional arguments behind flags.
//
// Go's flag package stops parsing at the first non-flag argument, so
// "auth alice -session x" would otherwise silently drop -session. Users
// reasonably write the subject of the command first, so accept both orders.
func reorderArgs(args []string, takesValue map[string]bool) []string {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Everything after "--" is positional by definition.
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}

		if !isFlag(arg) {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)

		// "-session=x" carries its own value; "-session x" consumes the next
		// argument.
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}

		if takesValue[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positional...)
}

func isFlag(arg string) bool {
	return len(arg) > 1 && strings.HasPrefix(arg, "-")
}
