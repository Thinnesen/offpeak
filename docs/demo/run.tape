# A short window: offpeak picks the start, creates the Job, and mirrors its phase.
Output docs/demo/run.mp4

Require kubectl
Set Shell "bash"
Set FontSize 17
Set Width 1400
Set Height 460
Set Padding 26
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 28ms
Set Framerate 12
Set PlaybackSpeed 4

Hide
Type "source /vhs/docs/demo/setup.sh" Enter
Sleep 2s
Show

Type "kubectl apply -f nightly-report.yaml" Enter
Sleep 2s
Type "watch -t -n 2 'kubectl get djob; echo; kubectl get jobs,pods'" Enter
Sleep 150s
Ctrl+C
Sleep 2s
