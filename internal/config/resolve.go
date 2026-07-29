package config

// Resolve returns the first non-empty value in precedence order: explicit
// flag > environment > config file > built-in default (CONF-03).
func Resolve(flagValue, envValue, configValue, builtin string) string {
	return ""
}
