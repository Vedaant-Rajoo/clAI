# Configure CLAI_ZSH_BINDING before sourcing this file to change the key binding.
# The default uses Zsh bindkey notation: ^X^A
function __clai_widget() {
    local clai_command=${CLAI_COMMAND:-clai}
    local tmpdir=/tmp
    local old_umask
    local result_file
    local widget_status
    local result=''

    old_umask=$(umask)
    umask 077
    result_file=$(command mktemp "$tmpdir/clai-widget.XXXXXX")
    local mktemp_status=$?
    umask "$old_umask"

    if ((mktemp_status != 0)) || [[ -z $result_file ]]; then
        return 1
    fi

    if ! command chmod 600 "$result_file"; then
        command rm -f -- "$result_file"
        return 1
    fi

    command "$clai_command" widget --shell zsh --result-file "$result_file"
    widget_status=$?

    if ((widget_status == 0)) && [[ -s $result_file ]]; then
        IFS= read -r -d $'\0' result < "$result_file" || true
    fi

    command rm -f -- "$result_file"

    if ((widget_status == 0)) && [[ -n $result ]]; then
        # Insert at the cursor so any text already on the prompt is preserved.
        LBUFFER+=$result
    fi

    zle redisplay
    if ((widget_status == 3)); then
        return 0
    fi
    return "$widget_status"
}

if [[ -o interactive ]]; then
    zle -N clai-widget __clai_widget

    typeset -g __clai_zsh_binding=${CLAI_ZSH_BINDING:-'^X^A'}
    typeset __clai_zsh_existing_binding
    __clai_zsh_existing_binding=$(bindkey "$__clai_zsh_binding" 2>/dev/null)

    if [[ $__clai_zsh_existing_binding == *' clai-widget' ]]; then
        :
    elif [[ -z $__clai_zsh_existing_binding || $__clai_zsh_existing_binding == *' undefined-key' ]]; then
        bindkey "$__clai_zsh_binding" clai-widget
    else
        print -u2 -r -- "clai: Zsh binding $__clai_zsh_binding is already in use; set CLAI_ZSH_BINDING to another sequence"
    fi

    unset __clai_zsh_binding __clai_zsh_existing_binding
fi
