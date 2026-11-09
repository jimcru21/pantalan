package domain

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// An App's env vars are written and read as dotenv text: one KEY=value
// pair per line. Blank lines and lines starting with "#" are ignored, so
// a whole .env file can be pasted in. The value is everything after the
// first "=", kept as written (quotes included), as docker's --env-file
// does. Comments aren't kept: the text read back is the pairs alone,
// sorted by name.

// ParseEnv reads dotenv text into env vars. The error is a *FieldError on
// "env" naming the line number of the first bad line.
func ParseEnv(text string) (map[string]string, error) {
	env := map[string]string{}
	seen := map[string]int{} // name -> its line
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		line = strings.TrimSuffix(line, "\r") // pasted with Windows line ends
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimLeft(line, " \t"), "=")
		if !ok {
			return nil, InvalidField("env", fmt.Sprintf("line %d: %q is not KEY=value", n, trimmed))
		}
		if err := checkEnvName(name); err != nil {
			return nil, InvalidField("env", fmt.Sprintf("line %d: %s", n, err))
		}
		if first, dup := seen[name]; dup {
			return nil, InvalidField("env", fmt.Sprintf("line %d: %s is already set on line %d", n, name, first))
		}
		seen[name] = n
		env[name] = value
	}
	return env, nil
}

// FormatEnv writes env vars as dotenv text, sorted by name, one line each
// ending in "\n". No env vars is "".
func FormatEnv(env map[string]string) string {
	var b strings.Builder
	for _, name := range EnvNames(env) {
		b.WriteString(name + "=" + env[name] + "\n")
	}
	return b.String()
}

// EnvNames are the names of env, sorted. It is never nil.
func EnvNames(env map[string]string) []string {
	names := slices.Sorted(maps.Keys(env))
	if names == nil {
		return []string{}
	}
	return names
}

// ValidateEnv checks env vars against the rules ParseEnv applies, so any
// env vars an App holds read back as the same text.
func ValidateEnv(env map[string]string) error {
	for _, name := range EnvNames(env) {
		if err := checkEnvName(name); err != nil {
			return InvalidField("env", err.Error())
		}
		if strings.ContainsAny(env[name], "\r\n") {
			return InvalidField("env", fmt.Sprintf("the value of %s has a line break", name))
		}
	}
	return nil
}

// checkEnvName: a name that starts with "-" would be read as a docker
// flag, and one that starts with "#" as a comment.
func checkEnvName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("an env var needs a name before the =")
	case strings.ContainsAny(name, " \t\r\n\v\f"):
		return fmt.Errorf("env name %q has a space in it", name)
	case strings.HasPrefix(name, "-"), strings.HasPrefix(name, "#"), strings.Contains(name, "="):
		return fmt.Errorf("env name %q is invalid", name)
	}
	return nil
}
