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
// print-to-stdout) through the flag > env > config > built-in chain
// (CONF-03). Booleans cannot use the empty-string sentinel Resolve relies on:
// a parsed false is indistinguishable from unset, so the caller passes
// explicit-set state collected via flag.Visit, and explicit-set state — never
// value comparison — decides. If either delivery flag was explicitly set, the
// flag layer wins entirely and the flag values are returned as-is, so an
// explicit --copy=false beats a config "clipboard" even though false equals
// the flag's default. Otherwise the first non-empty of env then config maps:
// "clipboard" -> copy, "stdout" -> print. The value "insert" is widget-only
// and has no interactive-mode meaning in Phase 1 — it, like any unknown
// value, resolves to neither behavior (mapping completed in Phase 3/4). With
// nothing set anywhere the built-in default is neither copy nor print,
// preserving current behavior. Pure function: the caller passes environment
// values in, so this package never reads process state.
func ResolveDelivery(copyFlag, printFlag, copyExplicit, printExplicit bool, envValue, configValue string) (copyOut, printOut bool) {
	if copyExplicit || printExplicit {
		return copyFlag, printFlag
	}
	value := envValue
	if value == "" {
		value = configValue
	}
	switch value {
	case "clipboard":
		return true, false
	case "stdout":
		return false, true
	default:
		return false, false
	}
}
