package notify

func platformCommand(lookPath func(string) (string, error), n Notification) *Cmd {
	return linuxCommand(lookPath, n)
}
