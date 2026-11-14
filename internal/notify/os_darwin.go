package notify

func platformCommand(_ func(string) (string, error), n Notification) *Cmd {
	c := darwinCommand(n)
	return &c
}
