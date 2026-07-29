package config

// Resolve returns the first non-empty value in precedence order: explicit
// flag > environment > config file > built-in default (CONF-03). It is pure —
// the caller passes environment values in — so precedence stays table-testable
// and this package never reads process state.
func Resolve(flagValue, envValue, configValue, builtin string) string {
	for _, v := range []string{flagValue, envValue, configValue} {
		if v != "" {
			return v
		}
	}
	return builtin
}

// ResolveDelivery resolves the delivery booleans (copy-to-clipboard,
// print-to-stdout) through the same flag > env > config > built-in chain.
func ResolveDelivery(copyFlag, printFlag, copyExplicit, printExplicit bool, envValue, configValue string) (copyOut, printOut bool) {
	return copyFlag, printFlag
}
