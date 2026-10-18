# Spot first, on-demand before the deadline. The demo cluster has no spot capacity,
# so the spot Job stays pending and offpeak starts the on-demand fallback in time.
Output docs/demo/fallback.mp4

Require kubectl
Set Shell "bash"
Set FontSize 17
Set Width 1400
Set Height 540
Set Padding 26
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 28ms
Set Framerate 12
Set PlaybackSpeed 6

Hide
Type "source /vhs/docs/demo/setup.sh" Enter
Sleep 2s
Show

Type "sed -n '/karpenter/,/fallbackMargin/p' gpu-batch.yaml" Enter
Sleep 4s
Type "kubectl apply -f gpu-batch.yaml" Enter
Sleep 2s
Type "watch -t -n 2 'kubectl get djob; echo; kubectl get jobs,pods'" Enter
Sleep 200s
Ctrl+C
Type "clear; kubectl get djob gpu-batch -o yaml | grep -A10 attempts:" Enter
Sleep 24s
