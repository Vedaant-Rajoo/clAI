function __clai_insert_command --description 'insert a clai command suggestion'
    set -q CLAI_COMMAND; or set -g CLAI_COMMAND clai

    set -l cmd ($CLAI_COMMAND --print-command)

    if test -n "$cmd"
        commandline -i -- "$cmd"
    end

    commandline -f repaint
end

if status is-interactive
    set -q CLAI_BINDING; or set -g CLAI_BINDING \cx\ca
    bind $CLAI_BINDING __clai_insert_command
end
