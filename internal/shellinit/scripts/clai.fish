# Configure CLAI_FISH_BINDING before sourcing this file to change the key binding.
# CLAI_BINDING is also accepted for compatibility with earlier Fish integration.
function __clai_widget --description 'open the clai command widget'
    set -l clai_command clai
    if set -q CLAI_COMMAND
        set clai_command $CLAI_COMMAND
    end

    set -l tmpdir /tmp

    set -l old_umask (umask)
    umask 077
    set -l result_file (command mktemp "$tmpdir/clai-widget.XXXXXX")
    set -l mktemp_status $status
    umask $old_umask

    if test $mktemp_status -ne 0; or test -z "$result_file"
        return 1
    end

    if not command chmod 600 "$result_file"
        command rm -f -- "$result_file"
        return 1
    end

    command "$clai_command" widget --shell fish --result-file "$result_file"
    set -l widget_status $status
    set -l result

    if test $widget_status -eq 0; and test -s "$result_file"
        set result (string collect --no-trim-newlines < "$result_file")
    end

    command rm -f -- "$result_file"

    if test $widget_status -eq 0; and test -n "$result"
        commandline --replace -- "$result"
        commandline --cursor (string length -- "$result")
    end

    commandline --function repaint
    if test $widget_status -eq 3
        return 0
    end
    return $widget_status
end

if status is-interactive
    set -l clai_binding \cx\ca
    set -l clai_binding_label '\cx\ca'
    if set -q CLAI_FISH_BINDING
        set clai_binding (string unescape -- "$CLAI_FISH_BINDING")
        set clai_binding_label $CLAI_FISH_BINDING
    else if set -q CLAI_BINDING
        set clai_binding (string unescape -- "$CLAI_BINDING")
        set clai_binding_label $CLAI_BINDING
    end

    set -l existing_binding (bind "$clai_binding" 2>/dev/null)
    if test $status -eq 0
        if not string match --quiet '*__clai_widget*' -- "$existing_binding"
            printf 'clai: Fish binding %s is already in use; set CLAI_FISH_BINDING to another sequence\n' "$clai_binding_label" >&2
        end
    else
        bind "$clai_binding" __clai_widget
    end
end
