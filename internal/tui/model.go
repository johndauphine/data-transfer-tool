package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/johndauphine/mssql-pg-migrate/internal/config"
	"github.com/johndauphine/mssql-pg-migrate/internal/orchestrator"
	"gopkg.in/yaml.v3"
)

type sessionMode int

const (
	modeNormal sessionMode = iota
	modeWizard
)

type wizardStep int

const (
	stepSourceType wizardStep = iota
	stepSourceHost
	stepSourcePort
	stepSourceDB
	stepSourceUser
	stepSourcePass
	stepSourceSSL
	stepTargetType
	stepTargetHost
	stepTargetPort
	stepTargetDB
	stepTargetUser
	stepTargetPass
	stepTargetSSL
	stepWorkers
	stepDone
)

// Model is the main TUI model
type Model struct {
	viewport    viewport.Model
	textInput   textinput.Model
	ready       bool
	gitInfo     GitInfo
	cwd         string
	err         error
	width       int
	height      int
	history     []string
	historyIdx  int
	logBuffer   string // Persistent buffer for logs
	lineBuffer  string // Buffer for incoming partial lines

	// Wizard state
	mode        sessionMode
	step        wizardStep
	wizardData  config.Config
	wizardInput string
}

// TickMsg is used to update the UI periodically (e.g. for git status)
type TickMsg time.Time

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second*5, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// InitialModel returns the initial model state
func InitialModel() Model {
	ti := textinput.New()
	ti.Placeholder = "Type /help for commands..."
	ti.Focus()
	ti.CharLimit = 156
	ti.Width = 20
	ti.Prompt = "❯ "
	ti.PromptStyle = stylePrompt

	cwd, _ := os.Getwd()

	m := Model{
		textInput:  ti,
		gitInfo:    GetGitInfo(),
		cwd:        cwd,
		history:    []string{},
		historyIdx: -1,
	}

	return m
}

// Update handles messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			value := m.textInput.Value()
			if m.mode == modeWizard {
				return m, m.handleWizardStep(value)
			}
			if value != "" {
				m.logBuffer += styleUserInput.Render("> "+value) + "\n"
				m.viewport.SetContent(m.logBuffer)
				m.viewport.GotoBottom()
				
				m.textInput.Reset()
				m.history = append(m.history, value)
				m.historyIdx = len(m.history)
				return m, m.handleCommand(value)
			}
		case tea.KeyUp:
			if m.historyIdx > 0 {
				m.historyIdx--
				m.textInput.SetValue(m.history[m.historyIdx])
			}
		case tea.KeyDown:
			if m.historyIdx < len(m.history)-1 {
				m.historyIdx++
				m.textInput.SetValue(m.history[m.historyIdx])
			} else {
				m.historyIdx = len(m.history)
				m.textInput.Reset()
			}
		case tea.KeyTab: // Command completion
			if m.mode == modeNormal {
				m.autocompleteCommand()
			}
		}

	case tea.WindowSizeMsg:
		headerHeight := 0
		footerHeight := 4 // Bordered input (3) + Status bar (1)
		verticalMarginHeight := headerHeight + footerHeight

		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-verticalMarginHeight)
			m.viewport.YPosition = headerHeight
			// Initialize log buffer with welcome message
			m.logBuffer = m.welcomeMessage()
			m.viewport.SetContent(m.logBuffer)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - verticalMarginHeight
		}
		m.width = msg.Width
		m.height = msg.Height
		m.textInput.Width = msg.Width - 4

	case OutputMsg:
		m.lineBuffer += string(msg)

		// Process complete lines
		for {
			newlineIdx := strings.Index(m.lineBuffer, "\n")
			if newlineIdx == -1 {
				break
			}

			// Extract line
			line := m.lineBuffer[:newlineIdx]
			m.lineBuffer = m.lineBuffer[newlineIdx+1:]

			// Handle carriage returns (simulate line overwrite by taking last part)
			if lastCR := strings.LastIndex(line, "\r"); lastCR != -1 {
				line = line[lastCR+1:]
			}

			// Apply styling
			lowerText := strings.ToLower(line)
			prefix := "  "
			if strings.Contains(lowerText, "error") || strings.Contains(lowerText, "fail") {
				line = styleError.Render(line)
				prefix = styleError.Render("✖ ")
			} else if strings.Contains(lowerText, "success") || strings.Contains(lowerText, "passed") {
				line = styleSuccess.Render(line)
				prefix = styleSuccess.Render("✔ ")
			} else {
				line = styleSystemOutput.Render(line)
			}

			// Append to log
			m.logBuffer += prefix + line + "\n"
		}
		
		// Update viewport
		m.viewport.SetContent(m.logBuffer)
		m.viewport.GotoBottom()

	case TickMsg:
		m.gitInfo = GetGitInfo()
		return m, tickCmd()
	}

	m.textInput, tiCmd = m.textInput.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	return m, tea.Batch(tiCmd, vpCmd)
}

// autocompleteCommand attempts to complete the current input
func (m *Model) autocompleteCommand() {
	input := m.textInput.Value()
	commands := []string{"/run", "/validate", "/status", "/history", "/wizard", "/logs", "/clear", "/quit", "/help"}
	
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, input) {
			m.textInput.SetValue(cmd)
			// Move cursor to end
			m.textInput.SetCursor(len(cmd))
			return
		}
	}
}

// View renders the TUI
func (m Model) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}

	return fmt.Sprintf("%s\n%s\n%s",
		m.viewport.View(),
		styleInputContainer.Width(m.width-2).Render(m.textInput.View()),
		m.statusBarView(),
	)
}

func (m Model) statusBarView() string {
	w := lipgloss.Width

	dir := styleStatusDir.Render(m.cwd)
	branch := styleStatusBranch.Render(" " + m.gitInfo.Branch)

	status := ""
	if m.gitInfo.Status == "Dirty" {
		status = styleStatusDirty.Render("Uncommitted Changes") 
	} else {
		status = styleStatusClean.Render("All Changes Committed") 
	}

	// Calculate remaining width for spacer
	usedWidth := w(dir) + w(branch) + w(status)
	if usedWidth > m.width {
		usedWidth = m.width
	}
	
	spacer := styleStatusBar.
		Width(m.width - usedWidth).
		Render("")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		dir,
		branch,
		spacer,
		status,
	)
}

func (m Model) welcomeMessage() string {
	logo := `
  __  __  _____  _____  _____ _       
 |  \/  |/ ____|/ ____|/ ____| |      
 | \  / | (___ | (___ | |  __| |      
 | |\/| |\___ \ \___ \ | | |_ | |      
 | |  | |____) |____) | |__| | |____  
 |_|  |_|_____/|_____/ \_____|______| 
  PG-MIGRATE INTERACTIVE SHELL
`
	
	welcome := styleTitle.Render(logo)
	
	body := `
 Welcome to the migration engine. This tool allows you to
 safely and efficiently move data between SQL Server and 
 PostgreSQL.
 
 Type /help to see available commands.
`
	
tips := lipgloss.NewStyle().Foreground(colorGray).Render("\n Tip: You can resume an interrupted migration with /run.")

	return welcome + body + tips
}

func (m Model) handleCommand(cmdStr string) tea.Cmd {
	parts := strings.Fields(cmdStr)
	if len(parts) == 0 {
		return nil
	}

	cmd := parts[0]

	switch cmd {
	case "/quit", "/exit":
		return tea.Quit

	case "/clear":
		m.logBuffer = m.welcomeMessage()
		m.viewport.SetContent(m.logBuffer)
		return nil

	case "/help":
		help := `
Available Commands:
  /wizard               Launch the configuration wizard
  /run [config_file]    Start migration (default: config.yaml)
  /validate             Validate migration
  /status               Show migration status
  /history              Show migration history
  /logs                 Save session logs to a file for analysis
  /clear                Clear screen
  /quit                 Exit application
`
		return func() tea.Msg { return OutputMsg(help) }

	case "/logs":
		logFile := "session.log"
		err := os.WriteFile(logFile, []byte(m.logBuffer), 0644)
		if err != nil {
			return func() tea.Msg { return OutputMsg(fmt.Sprintf("Error saving logs: %v\n", err)) }
		}
		return func() tea.Msg { return OutputMsg(fmt.Sprintf("Logs saved to %s\n", logFile)) }

	case "/wizard":
		m.mode = modeWizard
		m.step = stepSourceType
		m.textInput.Reset()
		m.textInput.Placeholder = ""
		m.logBuffer += "\n--- CONFIGURATION WIZARD ---\n"
		m.viewport.SetContent(m.logBuffer)
		return m.handleWizardStep("")

	case "/run":
		configFile := "config.yaml"
		if len(parts) > 1 {
			configFile = parts[1]
		}
		return m.runMigrationCmd(configFile)
	
	case "/validate":
		configFile := "config.yaml"
		if len(parts) > 1 {
			configFile = parts[1]
		}
		return m.runValidateCmd(configFile)

	case "/status":
		configFile := "config.yaml"
		if len(parts) > 1 {
			configFile = parts[1]
		}
		return m.runStatusCmd(configFile)
		
	case "/history":
		configFile := "config.yaml"
		if len(parts) > 1 {
			configFile = parts[1]
		}
		return m.runHistoryCmd(configFile)

	default:
		return func() tea.Msg { return OutputMsg("Unknown command: " + cmd + "\n") }
	}
}

// Wrappers for Orchestrator actions

func (m Model) runMigrationCmd(configFile string) tea.Cmd {
	return func() tea.Msg {
		// Output to the view
		out := fmt.Sprintf("Running migration with config: %s\n", configFile)
		
		// Check file existence first for better error
		if _, err := os.Stat(configFile); os.IsNotExist(err) {
			return OutputMsg(out + "Config file not found. Run /wizard to create one.\n")
		}

		// Load config
		cfg, err := config.Load(configFile)
		if err != nil {
			return OutputMsg(out + fmt.Sprintf("Error loading config: %v\n", err))
		}

		// Create orchestrator
		orch, err := orchestrator.New(cfg)
		if err != nil {
			return OutputMsg(out + fmt.Sprintf("Error initializing orchestrator: %v\n", err))
		}
		defer orch.Close()

		// Run
		if err := orch.Run(context.Background()); err != nil {
			return OutputMsg(out + fmt.Sprintf("Migration failed: %v\n", err))
		}
		
		return OutputMsg(out + "Migration completed successfully!\n")
	}
}

func (m Model) runValidateCmd(configFile string) tea.Cmd {
	return func() tea.Msg {
		out := fmt.Sprintf("Validating with config: %s\n", configFile)
		if _, err := os.Stat(configFile); os.IsNotExist(err) {
			return OutputMsg(out + "Config file not found. Run /wizard to create one.\n")
		}
		cfg, err := config.Load(configFile)
		if err != nil {
			return OutputMsg(out + fmt.Sprintf("Error: %v\n", err))
		}
		orch, err := orchestrator.New(cfg)
		if err != nil {
			return OutputMsg(out + fmt.Sprintf("Error: %v\n", err))
		}
		defer orch.Close()

		if err := orch.Validate(context.Background()); err != nil {
			return OutputMsg(out + fmt.Sprintf("Validation failed: %v\n", err))
		}
		return OutputMsg(out + "Validation passed!\n")
	}
}

func (m Model) runStatusCmd(configFile string) tea.Cmd {
	return func() tea.Msg {
		if _, err := os.Stat(configFile); os.IsNotExist(err) {
			return OutputMsg("Config file not found. Run /wizard to create one.\n")
		}
		cfg, err := config.Load(configFile)
		if err != nil {
			return OutputMsg(fmt.Sprintf("Error: %v\n", err))
		}
		orch, err := orchestrator.New(cfg)
		if err != nil {
			return OutputMsg(fmt.Sprintf("Error: %v\n", err))
		}
		defer orch.Close()

		// Capture stdout for ShowStatus
		if err := orch.ShowStatus(); err != nil {
			return OutputMsg(fmt.Sprintf("Error showing status: %v\n", err))
		}
		return nil
	}
}

func (m Model) runHistoryCmd(configFile string) tea.Cmd {
	return func() tea.Msg {
		if _, err := os.Stat(configFile); os.IsNotExist(err) {
			return OutputMsg("Config file not found. Run /wizard to create one.\n")
		}
		cfg, err := config.Load(configFile)
		if err != nil {
			return OutputMsg(fmt.Sprintf("Error: %v\n", err))
		}
		orch, err := orchestrator.New(cfg)
		if err != nil {
			return OutputMsg(fmt.Sprintf("Error: %v\n", err))
		}
		defer orch.Close()

		if err := orch.ShowHistory(); err != nil {
			return OutputMsg(fmt.Sprintf("Error showing history: %v\n", err))
		}
		return nil
	}
}

func (m *Model) handleWizardStep(input string) tea.Cmd {
	if input != "" {
		m.logBuffer += styleUserInput.Render("> " + input) + "\n"
		m.viewport.SetContent(m.logBuffer)
		m.textInput.Reset()
	}

	// Capture input for current step before moving to next
	switch m.step {
	case stepSourceType:
		if input != "" {
			m.wizardData.Source.Type = input
			m.step = stepSourceHost
		}
	case stepSourceHost:
		if input != "" {
			m.wizardData.Source.Host = input
			m.step = stepSourcePort
		}
	case stepSourcePort:
		if input != "" {
			fmt.Sscanf(input, "%d", &m.wizardData.Source.Port)
			m.step = stepSourceDB
		}
	case stepSourceDB:
		if input != "" {
			m.wizardData.Source.Database = input
			m.step = stepSourceUser
		}
	case stepSourceUser:
		if input != "" {
			m.wizardData.Source.User = input
			m.step = stepSourcePass
		}
	case stepSourcePass:
		if input != "" {
			m.wizardData.Source.Password = input
			m.step = stepSourceSSL
			m.textInput.EchoMode = textinput.EchoNormal
		}
	case stepSourceSSL:
		if input != "" {
			if m.wizardData.Source.Type == "postgres" {
				m.wizardData.Source.SSLMode = input
			} else {
				if strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" || strings.ToLower(input) == "true" {
					m.wizardData.Source.TrustServerCert = true
				}
			}
			m.step = stepTargetType
		}
	case stepTargetType:
		if input != "" {
			m.wizardData.Target.Type = input
			m.step = stepTargetHost
		}
	case stepTargetHost:
		if input != "" {
			m.wizardData.Target.Host = input
			m.step = stepTargetPort
		}
	case stepTargetPort:
		if input != "" {
			fmt.Sscanf(input, "%d", &m.wizardData.Target.Port)
			m.step = stepTargetDB
		}
	case stepTargetDB:
		if input != "" {
			m.wizardData.Target.Database = input
			m.step = stepTargetUser
		}
	case stepTargetUser:
		if input != "" {
			m.wizardData.Target.User = input
			m.step = stepTargetPass
		}
	case stepTargetPass:
		if input != "" {
			m.wizardData.Target.Password = input
			m.step = stepTargetSSL
			m.textInput.EchoMode = textinput.EchoNormal
		}
	case stepTargetSSL:
		if input != "" {
			if m.wizardData.Target.Type == "postgres" {
				m.wizardData.Target.SSLMode = input
			} else {
				if strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" || strings.ToLower(input) == "true" {
					m.wizardData.Target.TrustServerCert = true
				}
			}
			m.step = stepWorkers
		}
	case stepWorkers:
		if input != "" {
			fmt.Sscanf(input, "%d", &m.wizardData.Migration.Workers)
			return m.finishWizard()
		}
	}

	// Display prompt for current (new) step
	var prompt string
	switch m.step {
	case stepSourceType:
		prompt = "Source Type (mssql/postgres) [mssql]: "
	case stepSourceHost:
		prompt = "Source Host: "
	case stepSourcePort:
		prompt = "Source Port [1433/5432]: "
	case stepSourceDB:
		prompt = "Source Database: "
	case stepSourceUser:
		prompt = "Source User: "
	case stepSourcePass:
		prompt = "Source Password: "
		m.textInput.EchoMode = textinput.EchoPassword
	case stepSourceSSL:
		if m.wizardData.Source.Type == "postgres" {
			prompt = "Source SSL Mode (disable/require/verify-ca/verify-full) [require]: "
		} else {
			prompt = "Trust Source Server Certificate? (y/n) [n]: "
		}
	case stepTargetType:
		prompt = "Target Type (postgres/mssql) [postgres]: "
	case stepTargetHost:
		prompt = "Target Host: "
	case stepTargetPort:
		prompt = "Target Port [5432/1433]: "
	case stepTargetDB:
		prompt = "Target Database: "
	case stepTargetUser:
		prompt = "Target User: "
	case stepTargetPass:
		prompt = "Target Password: "
		m.textInput.EchoMode = textinput.EchoPassword
	case stepTargetSSL:
		if m.wizardData.Target.Type == "postgres" {
			prompt = "Target SSL Mode (disable/require/verify-ca/verify-full) [require]: "
		} else {
			prompt = "Trust Target Server Certificate? (y/n) [n]: "
		}
	case stepWorkers:
		prompt = "Parallel Workers [8]: "
	}

	m.logBuffer += prompt
	m.viewport.SetContent(m.logBuffer)
	m.viewport.GotoBottom()
	return nil
}

func (m *Model) finishWizard() tea.Cmd {
	return func() tea.Msg {
		// Set some sensible defaults if missed
		if m.wizardData.Source.Type == "" {
			m.wizardData.Source.Type = "mssql"
		}
		if m.wizardData.Target.Type == "" {
			m.wizardData.Target.Type = "postgres"
		}
		if m.wizardData.Migration.Workers == 0 {
			m.wizardData.Migration.Workers = 8
		}

		data, err := yaml.Marshal(m.wizardData)
		if err != nil {
			m.mode = modeNormal
			return OutputMsg(fmt.Sprintf("\nError generating config: %v\n", err))
		}

		// Write to file
		if err := os.WriteFile("config.yaml", data, 0600); err != nil {
			m.mode = modeNormal
			return OutputMsg(fmt.Sprintf("\nError saving config.yaml: %v\n", err))
		}

		m.mode = modeNormal
		return OutputMsg("\nConfiguration saved to config.yaml!\nYou can now run the migration with /run.\n")
	}
}

// Start launches the TUI program
func Start() error {
	m := InitialModel()
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Start output capture
	cleanup := CaptureOutput(p)
	defer cleanup()

	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}