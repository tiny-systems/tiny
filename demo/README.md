# Demo GIF

The README hero is a scripted, cluster-free mockup rendered with
[VHS](https://github.com/charmbracelet/vhs). Two tmux panes on one timeline:
left = `tiny` serving + its streaming tool log, right = an agent (Claude Code)
prompting.

## Render

```sh
brew install vhs tmux      # one-time
vhs demo/demo.tape         # run from the repo root → writes demo/demo.gif
git add demo/demo.gif && git commit
```

## Preview without rendering

```sh
bash demo/run.sh           # opens the two-pane tmux session live
```

## Files

- `demo.tape` — VHS script (size, theme, timing)
- `run.sh` — tmux orchestration (the split)
- `mock-tiny.sh` / `mock-claude.sh` — the two panes, timed to move together

It's a mockup — no cluster or real agent runs. Tweak the `p "…" <seconds>` lines
in the two scripts to change copy or pacing; keep the timelines aligned so the
panes stay in sync.
