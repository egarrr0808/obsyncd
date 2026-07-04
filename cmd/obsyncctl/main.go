package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	appconfig "obsyncd/internal/config"
	"obsyncd/internal/update"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	markConflictStart = "%%OBSYNCD_CONFLICT_START%%"
	markLocalStart    = "%%OBSYNCD_LOCAL_START%%"
	markLocalEnd      = "%%OBSYNCD_LOCAL_END%%"
	markRemoteStart   = "%%OBSYNCD_REMOTE_START%%"
	markRemoteEnd     = "%%OBSYNCD_REMOTE_END%%"
	markConflictEnd   = "%%OBSYNCD_CONFLICT_END%%"
)

type statusArgs struct{}

type statusReply struct {
	FolderID        string
	FolderState     string
	FolderStateTime string
	OracleName      string
	OracleDeviceID  string
	OracleConnected bool
	ManualConflicts []string
	Pending         []pendingConflict
}

type pendingConflict struct {
	Canonical string
	Staged    string
}

type rescanArgs struct {
	Paths []string
}

type rescanReply struct {
	FolderID string
	Paths    []string
	OK       bool
}

type resolveArgs struct {
	Path   string
	Action string
}

type resolveReply struct {
	Path string
	OK   bool
}

type conflictFile struct {
	Path   string
	Rel    string
	Staged string
}

type conflictBlock struct {
	Start  int
	End    int
	Local  string
	Remote string
}

type mode int

const (
	modeDone mode = iota
	modeList
	modeDiff
	modeMessage
)

type model struct {
	socket  string
	vault   string
	files   []conflictFile
	cursor  int
	mode    mode
	width   int
	height  int
	message string
	err     error
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	localStyle  = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("36")).Padding(0, 1)
	remoteStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("214")).Padding(0, 1)
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func main() {
	defaultConfig := filepath.Join(os.Getenv("HOME"), ".config", "obsyncd", "config.yaml")
	socket := flag.String("socket", defaultSocketPath(), "obsyncd local socket")
	configPath := flag.String("config", defaultConfig, "obsyncd YAML config")
	flag.Parse()

	if err := update.MaybeRelaunch("obsyncctl", os.Args[1:]); err != nil && os.Getenv("OBSYNCD_UPDATE_VERBOSE") == "1" {
		fmt.Fprintln(os.Stderr, "obsyncctl update skipped:", err)
	}

	if flag.NArg() > 0 {
		runCommand(*socket, *configPath, flag.Args())
		return
	}

	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	files, err := loadPending(*socket, cfg.VaultPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(files) == 0 {
		fmt.Println("All files copied and synced successfully.")
		return
	}
	m := model{socket: *socket, vault: cfg.VaultPath, files: files, mode: modeList}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCommand(socket, configPath string, args []string) {
	client, err := dial(socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close()

	switch args[0] {
	case "status":
		var reply statusReply
		if err := client.Call("Daemon.Status", statusArgs{}, &reply); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("Folder: %s\n", reply.FolderID)
		fmt.Printf("State: %s", reply.FolderState)
		if reply.FolderStateTime != "" {
			fmt.Printf(" (%s)", reply.FolderStateTime)
		}
		fmt.Println()
		fmt.Printf("Oracle: %s\n", reply.OracleName)
		fmt.Printf("Device: %s\n", reply.OracleDeviceID)
		fmt.Printf("Connected: %t\n", reply.OracleConnected)
		if len(reply.ManualConflicts) == 0 {
			if cfg, err := appconfig.Load(configPath); err == nil {
				if files, err := scanPendingDir(cfg.VaultPath); err == nil {
					for _, file := range files {
						reply.ManualConflicts = append(reply.ManualConflicts, file.Rel)
					}
				}
			}
		}
		if len(reply.ManualConflicts) > 0 {
			fmt.Println("Awaiting User Resolution:")
			for _, path := range reply.ManualConflicts {
				fmt.Printf("  %s\n", path)
			}
		}
	case "rescan":
		var reply rescanReply
		if err := callRescan(client, args[1:], &reply); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("Rescan queued: %s\n", reply.FolderID)
		if len(reply.Paths) > 0 {
			fmt.Printf("Paths: %v\n", reply.Paths)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case editorDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.mode = modeMessage
			return m, nil
		}
		if err := resolvePending(m.socket, m.files[m.cursor].Rel, "manual"); err != nil {
			m.err = err
			m.mode = modeMessage
			return m, nil
		}
		return m.reload("Manual edit saved."), nil
	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			if m.mode == modeDiff {
				m.mode = modeList
				return m, nil
			}
		}
		switch m.mode {
		case modeDone:
			return m, tea.Quit
		case modeList:
			switch msg.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < len(m.files)-1 {
					m.cursor++
				}
			case "enter":
				m.mode = modeDiff
			}
		case modeDiff:
			switch strings.ToLower(msg.String()) {
			case "l":
				return m.resolve("local"), nil
			case "r":
				return m.resolve("remote"), nil
			case "s":
				return m.resolve("submerge"), nil
			case "m":
				editor := os.Getenv("EDITOR")
				if editor == "" {
					editor = "vi"
				}
				cmd := exec.Command(editor, m.files[m.cursor].Path)
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					return editorDoneMsg{err: err}
				})
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.err != nil {
		return errorStyle.Render(m.err.Error()) + "\n\nq quit\n"
	}
	switch m.mode {
	case modeList:
		var b strings.Builder
		b.WriteString(titleStyle.Render("obsyncd conflicts") + "\n\n")
		for i, file := range m.files {
			prefix := "  "
			if i == m.cursor {
				prefix = cursorStyle.Render("> ")
			}
			b.WriteString(prefix + file.Rel + "\n")
		}
		b.WriteString("\nenter open  q quit\n")
		return b.String()
	case modeDiff:
		return m.diffView()
	case modeMessage:
		return titleStyle.Render(m.message) + "\n\nq quit\n"
	default:
		return ""
	}
}

func (m model) diffView() string {
	file := m.files[m.cursor]
	localBytes, err := os.ReadFile(file.Path)
	if err != nil {
		return errorStyle.Render(err.Error()) + "\n"
	}
	remoteBytes, err := os.ReadFile(file.Staged)
	if err != nil {
		return errorStyle.Render(err.Error()) + "\n"
	}
	colWidth := max(20, (m.width-6)/2)
	left := localStyle.Width(colWidth).Render(string(localBytes))
	right := remoteStyle.Width(colWidth).Render(string(remoteBytes))
	header := titleStyle.Render(file.Rel) + "\n\n"
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	help := "\n\nL keep local  R keep remote  S submerge  M manual edit  esc back  q quit\n"
	return header + body + help
}

func (m model) resolve(action string) model {
	file := m.files[m.cursor]
	if err := resolvePending(m.socket, file.Rel, action); err != nil {
		m.err = err
		m.mode = modeMessage
		return m
	}
	return m.reload("Resolved " + file.Rel)
}

func (m model) reload(message string) model {
	files, err := loadPending(m.socket, m.vault)
	if err != nil {
		m.err = err
		m.mode = modeMessage
		return m
	}
	m.files = files
	if len(files) == 0 {
		m.mode = modeDone
		return m
	}
	if m.cursor >= len(files) {
		m.cursor = len(files) - 1
	}
	m.mode = modeList
	m.message = message
	return m
}

type editorDoneMsg struct {
	err error
}

func scanConflicts(root string) ([]conflictFile, error) {
	var files []conflictFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".markdown" {
			return nil
		}
		bs, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(bs), markConflictStart) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, conflictFile{Path: path, Rel: rel})
		return nil
	})
	return files, err
}

func loadPending(socket, root string) ([]conflictFile, error) {
	client, err := dial(socket)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var reply statusReply
	if err := client.Call("Daemon.Status", statusArgs{}, &reply); err != nil {
		return nil, err
	}
	files := make([]conflictFile, 0, len(reply.Pending))
	for _, p := range reply.Pending {
		files = append(files, conflictFile{
			Path:   filepath.Join(root, filepath.FromSlash(p.Canonical)),
			Rel:    p.Canonical,
			Staged: filepath.Join(root, filepath.FromSlash(p.Staged)),
		})
	}
	if len(files) == 0 {
		return scanPendingDir(root)
	}
	return files, nil
}

func scanPendingDir(root string) ([]conflictFile, error) {
	dir := filepath.Join(root, ".obsidian", "obsyncd-staging")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []conflictFile
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		bs, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var p pendingConflict
		if err := json.Unmarshal(bs, &p); err != nil {
			return nil, err
		}
		files = append(files, conflictFile{
			Path:   filepath.Join(root, filepath.FromSlash(p.Canonical)),
			Rel:    p.Canonical,
			Staged: filepath.Join(root, filepath.FromSlash(p.Staged)),
		})
	}
	return files, nil
}

func parseConflictBlocks(s string) []conflictBlock {
	var blocks []conflictBlock
	pos := 0
	for {
		start := strings.Index(s[pos:], markConflictStart)
		if start < 0 {
			return blocks
		}
		start += pos
		localStart := strings.Index(s[start:], markLocalStart)
		localEnd := strings.Index(s[start:], markLocalEnd)
		remoteStart := strings.Index(s[start:], markRemoteStart)
		remoteEnd := strings.Index(s[start:], markRemoteEnd)
		conflictEnd := strings.Index(s[start:], markConflictEnd)
		if localStart < 0 || localEnd < 0 || remoteStart < 0 || remoteEnd < 0 || conflictEnd < 0 {
			return blocks
		}
		localStart += start
		localEnd += start
		remoteStart += start
		remoteEnd += start
		conflictEnd += start + len(markConflictEnd)
		localTextStart := consumeLineEnd(s, localStart+len(markLocalStart))
		remoteTextStart := consumeLineEnd(s, remoteStart+len(markRemoteStart))
		blocks = append(blocks, conflictBlock{
			Start:  start,
			End:    consumeLineEnd(s, conflictEnd),
			Local:  s[localTextStart:localEnd],
			Remote: s[remoteTextStart:remoteEnd],
		})
		pos = conflictEnd
	}
}

func resolveContent(s, action string) (string, bool) {
	blocks := parseConflictBlocks(s)
	if len(blocks) == 0 {
		return s, false
	}
	var out strings.Builder
	pos := 0
	for _, block := range blocks {
		out.WriteString(s[pos:block.Start])
		switch action {
		case "local":
			out.WriteString(block.Local)
		case "remote":
			out.WriteString(block.Remote)
		case "submerge":
			out.WriteString(block.Local)
			if block.Local != "" && !strings.HasSuffix(block.Local, "\n") && block.Remote != "" {
				out.WriteString("\n")
			}
			out.WriteString(block.Remote)
		}
		pos = block.End
	}
	out.WriteString(s[pos:])
	return out.String(), true
}

func consumeLineEnd(s string, pos int) int {
	if pos < len(s) && s[pos] == '\r' {
		pos++
	}
	if pos < len(s) && s[pos] == '\n' {
		pos++
	}
	return pos
}

func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".obsyncctl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func rescanPath(socket, rel string) error {
	client, err := dial(socket)
	if err != nil {
		return err
	}
	defer client.Close()
	var reply rescanReply
	return callRescan(client, []string{rel}, &reply)
}

func resolvePending(socket, rel, action string) error {
	client, err := dial(socket)
	if err != nil {
		return err
	}
	defer client.Close()
	var reply resolveReply
	return client.Call("Daemon.Resolve", resolveArgs{Path: rel, Action: action}, &reply)
}

func callRescan(client *rpc.Client, paths []string, reply *rescanReply) error {
	return client.Call("Daemon.Rescan", rescanArgs{Paths: paths}, reply)
}

func dial(socket string) (*rpc.Client, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}
	return rpc.NewClient(conn), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: obsyncctl [-config path] [-socket path] [status|rescan [path...]]")
}

func defaultSocketPath() string {
	if path := os.Getenv("OBSYNCD_SOCKET"); path != "" {
		return path
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "obsyncd.sock")
	}
	return filepath.Join(dir, "obsyncd", "obsyncd.sock")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
