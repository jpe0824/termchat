package notify

import "strings"

// platformCommand is the command that posts n through goos's notification
// service, or nil when goos has none or none is installed. Building it may
// search PATH with lookPath.
func platformCommand(goos string, lookPath func(string) (string, error), n Notification) *command {
	switch goos {
	case "darwin":
		c := darwinCommand(n)
		return &c
	case "linux":
		return linuxCommand(lookPath, n)
	case "windows":
		c := windowsCommand(n)
		return &c
	}
	return nil
}

// The commands below are fixed scripts. The title and text are passed as
// separate arguments or environment variables for the program to read, never
// built into a script or command line.

const darwinScript = "on run argv\n" +
	"display notification (item 2 of argv) with title (item 1 of argv)\n" +
	"end run"

// darwinCommand posts through osascript. "--" ends option parsing so a title
// beginning with '-' is still an argument of the script.
func darwinCommand(n Notification) command {
	return command{Name: "osascript", Args: []string{"-e", darwinScript, "--", headline(n), n.Text}}
}

// linuxCommand uses notify-send when installed and otherwise calls the
// notification service directly with gdbus. It returns nil when neither is
// available.
func linuxCommand(lookPath func(string) (string, error), n Notification) *command {
	if _, err := lookPath("notify-send"); err == nil {
		return &command{Name: "notify-send", Args: []string{"--app-name=termchat", "--", headline(n), markupEscape(n.Text)}}
	}
	if _, err := lookPath("gdbus"); err == nil {
		return &command{Name: "gdbus", Args: []string{
			"call", "--session",
			"--dest", "org.freedesktop.Notifications",
			"--object-path", "/org/freedesktop/Notifications",
			"--method", "org.freedesktop.Notifications.Notify",
			gvariantString("termchat"), "0", gvariantString(""),
			gvariantString(headline(n)), gvariantString(markupEscape(n.Text)),
			"[]", "{}", "5000",
		}}
	}
	return nil
}

// markupEscape makes s plain text for a notification server, most of which
// render the body as markup: tags, links and, on some, local images. The
// title is plain text by the specification and is left alone. '&' goes first
// so the entities written for '<' and '>' are not escaped again.
func markupEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// gvariantString quotes s as a GVariant text-format string, which is how
// gdbus reads its arguments; the quoting keeps s a single string value.
func gvariantString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

const windowsScript = `$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
$esc = [System.Security.SecurityElement]
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml('<toast><visual><binding template="ToastGeneric"><text>' + $esc::Escape($env:TERMCHAT_TITLE) + '</text><text>' + $esc::Escape($env:TERMCHAT_TEXT) + '</text></binding></visual></toast>')
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('termchat').Show($toast)`

// windowsCommand shows a toast from PowerShell, reading the title and text
// from the environment.
func windowsCommand(n Notification) command {
	return command{
		Name: "powershell",
		Args: []string{"-NoProfile", "-NonInteractive", "-Command", windowsScript},
		Env:  []string{"TERMCHAT_TITLE=" + headline(n), "TERMCHAT_TEXT=" + n.Text},
	}
}
