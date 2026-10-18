# The hero demo: plan a job in the cleanest window before a deadline.
Output docs/demo/plan.mp4

Require kubectl
Set Shell "bash"
Set FontSize 17
Set Width 1400
Set Height 880
Set Padding 26
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 28ms
Set Framerate 12

Hide
Type "source /vhs/docs/demo/setup.sh" Enter
Sleep 2s
Show

Type "# Sources need no API key: a CO2 prognosis and a sun-and-wind forecast" Enter
Sleep 600ms
Type "kubectl get signalsources" Enter
Sleep 2s
Type "head -12 train-model.yaml" Enter
Sleep 4s
Type "kubectl apply -f train-model.yaml" Enter
Sleep 6s
Type "kubectl get djob" Enter
Sleep 3s
Type "kubectl get djob train-model -o yaml | grep -A5 estimates:" Enter
Sleep 6s
