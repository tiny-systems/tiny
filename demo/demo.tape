# VHS script for the README hero GIF — a two-pane mockup (no cluster needed).
# Render:  brew install vhs tmux  &&  vhs demo/demo.tape   (run from the repo root)
# Produces demo/demo.gif.

Output demo/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1500
Set Height 420
Set Padding 26
Set Theme "Catppuccin Mocha"

# Kick off both panes; then just let the choreography play out.
Type "bash demo/run.sh"
Enter
Sleep 16s
