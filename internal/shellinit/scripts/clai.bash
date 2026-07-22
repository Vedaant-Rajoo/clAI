# Configure CLAI_BASH_BINDING before sourcing this file to change the key binding.
# The value uses Bash readline notation, for example: \C-x\C-a
__clai_widget() {
    local clai_command=${CLAI_COMMAND:-clai}
    local tmpdir=/tmp
    local result_file
    local widget_status
    local result=

    result_file=$(umask 077 && command mktemp "$tmpdir/clai-widget.XXXXXX") || return 1
    if ! command chmod 600 "$result_file"; then
        command rm -f -- "$result_file"
        return 1
    fi

    command "$clai_command" widget --shell bash --result-file "$result_file"
    widget_status=$?

    if ((widget_status == 0)) && [[ -s $result_file ]]; then
        IFS= read -r -d '' result < "$result_file" || :
    fi

    command rm -f -- "$result_file"

    if ((widget_status == 0)) && [[ -n $result ]]; then
        # Insert at the cursor so any text already on the prompt is preserved.
        # READLINE_POINT counts characters in multibyte locales; splice with
        # substring expansion under the caller's locale to stay in character
        # units.
        READLINE_LINE=${READLINE_LINE:0:READLINE_POINT}$result${READLINE_LINE:READLINE_POINT}
        READLINE_POINT=$((READLINE_POINT + ${#result}))
    fi

    if ((widget_status == 3)); then
        return 0
    fi
    return "$widget_status"
}

# Bash 3.x has bind -x but does not expose writable READLINE_LINE and
# READLINE_POINT. This helper is used by a Readline macro based on the same
# compatibility technique as fzf: stash the current buffer in the kill ring,
# run clai, and yank the original text back before any accepted command so
# existing prompt text is preserved.
__clai_widget_bash3() {
    local clai_command=${CLAI_COMMAND:-clai}
    local tmpdir=/tmp
    local result_file
    local widget_status

    result_file=$(umask 077 && command mktemp "$tmpdir/clai-widget.XXXXXX") || return 0
    if ! command chmod 600 "$result_file"; then
        command rm -f -- "$result_file"
        return 0
    fi

    command "$clai_command" widget --shell bash --result-file "$result_file"
    widget_status=$?

    if ((widget_status == 0)) && [[ -s $result_file ]]; then
        command cat -- "$result_file"
    fi

    command rm -f -- "$result_file"
    # A nonzero command substitution aborts the remaining Readline macro and
    # loses the saved buffer. Always succeed here; empty stdout means restore.
    return 0
}

if [[ $- == *i* ]]; then
    __clai_bash_binding=${CLAI_BASH_BINDING:-'\C-x\C-a'}

    if [[ ${__CLAI_BASH_BOUND_KEY-} != "$__clai_bash_binding" ]]; then
        __clai_bash_binding_in_use=false
        __clai_bash_supported_mode=true

        if ((BASH_VERSINFO[0] < 4)); then
            if ! set -o | command grep -Eq '^emacs[[:space:]]+on$'; then
                __clai_bash_supported_mode=false
            fi
            case $(bind -m emacs-standard -P 2>/dev/null) in
                *"$__clai_bash_binding"*) __clai_bash_binding_in_use=true ;;
            esac
            case $(bind -m emacs-standard -S 2>/dev/null) in
                *"$__clai_bash_binding "*) __clai_bash_binding_in_use=true ;;
            esac
        else
            case $(bind -P 2>/dev/null) in
                *"$__clai_bash_binding"*) __clai_bash_binding_in_use=true ;;
            esac
            case $(bind -S 2>/dev/null) in
                *"$__clai_bash_binding "*) __clai_bash_binding_in_use=true ;;
            esac
            case $(bind -X 2>/dev/null) in
                *"$__clai_bash_binding"*) __clai_bash_binding_in_use=true ;;
            esac
        fi

        if [[ $__clai_bash_supported_mode != true ]]; then
            printf 'clai: Bash 3 requires Emacs editing mode; run set -o emacs before loading the integration\n' >&2
        elif [[ $__clai_bash_binding_in_use == true ]]; then
            printf 'clai: Bash binding %s is already in use; set CLAI_BASH_BINDING to another sequence\n' "$__clai_bash_binding" >&2
        elif ((BASH_VERSINFO[0] < 4)); then
            bind -m emacs-standard '"'"$__clai_bash_binding"'": "\C-a\C-k`__clai_widget_bash3`\e\C-e\C-a\C-y\C-e"'
            __CLAI_BASH_BOUND_KEY=$__clai_bash_binding
        else
            bind -x "\"$__clai_bash_binding\":__clai_widget"
            __CLAI_BASH_BOUND_KEY=$__clai_bash_binding
        fi
        unset __clai_bash_binding_in_use __clai_bash_supported_mode
    fi

    unset __clai_bash_binding
fi
