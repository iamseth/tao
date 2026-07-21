package cli

import (
	"errors"
	"flag"
	"strconv"
	"strings"
)

func (a App) flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	return fs
}

func (a App) parseArgs(name string, args []string, register func(*flag.FlagSet)) (*flag.FlagSet, []string, error) {
	fs := a.flagSet(name)
	if register != nil {
		register(fs)
	}
	valueFlags := map[string]bool{}
	fs.VisitAll(func(fl *flag.Flag) {
		if boolValue, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && boolValue.IsBoolFlag() {
			return
		}
		valueFlags["--"+fl.Name] = true
	})
	flagArgs, positional := splitSubcommandFlags(args, valueFlags)
	if err := fs.Parse(flagArgs); err != nil {
		return fs, positional, err
	}
	return fs, positional, nil
}

func (a App) parseArgsFor(metadata *commandMetadata, args []string) (*flag.FlagSet, []string, error) {
	name := ""
	var register func(*flag.FlagSet)
	if metadata != nil {
		name = metadata.name
		register = metadata.registerFlags
	}
	return a.parseArgs(name, args, register)
}

func requireNoArgs(args []string, usage string) error {
	if len(args) != 0 {
		return errors.New(usage)
	}
	return nil
}

func requirePositionals(args []string, count int, usage string) error {
	if len(args) != count {
		return errors.New(usage)
	}
	return nil
}

func flagWasProvided(fs *flag.FlagSet, name string) bool {
	provided := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			provided = true
		}
	})
	return provided
}

func flagIntValue(fs *flag.FlagSet, name string) int {
	fl := fs.Lookup(name)
	if fl == nil {
		return 0
	}
	if getter, ok := fl.Value.(flag.Getter); ok {
		if value, ok := getter.Get().(int); ok {
			return value
		}
	}
	value, _ := strconv.Atoi(fl.Value.String())
	return value
}

func flagStringValue(fs *flag.FlagSet, name string) string {
	fl := fs.Lookup(name)
	if fl == nil {
		return ""
	}
	if getter, ok := fl.Value.(flag.Getter); ok {
		if value, ok := getter.Get().(string); ok {
			return value
		}
	}
	return fl.Value.String()
}

func flagBoolValue(fs *flag.FlagSet, name string) bool {
	fl := fs.Lookup(name)
	if fl == nil {
		return false
	}
	if getter, ok := fl.Value.(flag.Getter); ok {
		if value, ok := getter.Get().(bool); ok {
			return value
		}
	}
	value, _ := strconv.ParseBool(fl.Value.String())
	return value
}

func effectiveBoolFlagValue(fs *flag.FlagSet, name string, fallback bool) bool {
	if !flagWasProvided(fs, name) {
		return fallback
	}
	return flagBoolValue(fs, name)
}

func splitSubcommandFlags(args []string, valueFlags map[string]bool) ([]string, []string) {
	flagArgs := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		name := arg
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if valueFlags[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return flagArgs, positional
}
