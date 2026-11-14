package notify

func platformCommand(_ func(string) (string, error), n Notification) *Cmd {
	c := windowsCommand(n)
	return &c
}
