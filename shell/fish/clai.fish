function __clai_insert_command --description 'insert a clai command suggestion'
    set -l cmd (clai --print-command)

    if test -n "$cmd"
        commandline -i -- "$cmd"
    end

    commandline -f repaint
end

if status is-interactive
    bind \cx\ca __clai_insert_command
end
