package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Allrivenjs/vozgo/internal/audio"
	"github.com/Allrivenjs/vozgo/internal/config"
	"github.com/Allrivenjs/vozgo/internal/transcribe"
)

// mcpServer expone vozgo a un agente (Claude Code, Codex...) por stdio.
// El agente pide una ruta y recibe el texto: no hay HTTP, ni puertos, ni
// archivos intermedios que el agente tenga que ir a leer.
type mcpServer struct {
	svc *transcribe.Service
	cfg config.Config
	// root, si no está vacío, acota qué rutas puede tocar el agente.
	root string
}

// --- entradas y salidas de las herramientas -----------------------------------

type transcribeInput struct {
	Path      string `json:"path" jsonschema:"ruta absoluta a un archivo de audio o a una carpeta con audios"`
	Language  string `json:"language,omitempty" jsonschema:"idioma ISO como es o en; auto para detectarlo"`
	Prompt    string `json:"prompt,omitempty" jsonschema:"vocabulario propio (nombres, siglas, jerga) para sesgar el decodificado"`
	Recursive *bool  `json:"recursive,omitempty" jsonschema:"si la ruta es una carpeta, recorrer subcarpetas; por defecto sí"`
}

type transcriptItem struct {
	Filename   string `json:"filename"`
	Text       string `json:"text"`
	Language   string `json:"language,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

type transcribeOutput struct {
	Items []transcriptItem `json:"items" jsonschema:"una entrada por audio, en orden"`
	Text  string           `json:"text" jsonschema:"todas las transcripciones unidas, en orden"`
	Model string           `json:"model"`
}

type infoOutput struct {
	Model    string   `json:"model"`
	Models   []string `json:"models_available"`
	Language string   `json:"language"`
	Workers  int      `json:"workers"`
	Threads  int      `json:"threads"`
	Formats  []string `json:"formats"`
	Root     string   `json:"root,omitempty" jsonschema:"si está definido, solo se pueden transcribir rutas bajo esta carpeta"`
	Prompt   string   `json:"prompt,omitempty"`
}

// --- herramientas -------------------------------------------------------------

func (m *mcpServer) transcribeTool(ctx context.Context, _ *mcp.CallToolRequest, in transcribeInput) (*mcp.CallToolResult, transcribeOutput, error) {
	path, err := m.resolve(in.Path)
	if err != nil {
		return nil, transcribeOutput{}, err
	}

	recursive := true
	if in.Recursive != nil {
		recursive = *in.Recursive
	}
	inputs, err := collect([]string{path}, recursive)
	if err != nil {
		return nil, transcribeOutput{}, err
	}
	inputs, _ = dedupe(inputs)
	if len(inputs) == 0 {
		return nil, transcribeOutput{}, fmt.Errorf("no se encontraron audios en %s (extensiones: %s)",
			path, strings.Join(audio.Extensions, " "))
	}

	// language y prompt son por llamada, así que se usa un servicio propio
	// cuando difieren de los del arranque.
	svc := m.svc
	if in.Language != "" || in.Prompt != "" {
		cfg := m.cfg
		if in.Language != "" {
			cfg.Language = in.Language
		}
		if in.Prompt != "" {
			cfg.Prompt = in.Prompt
		}
		svc, err = transcribe.New(cfg)
		if err != nil {
			return nil, transcribeOutput{}, err
		}
		svc.Start(ctx)
		defer svc.Wait()
	}

	ids := make([]string, 0, len(inputs))
	for _, file := range inputs {
		id, err := svc.Submit(transcribe.Request{SourcePath: file, Filename: filepath.Base(file)})
		if err != nil {
			return nil, transcribeOutput{}, err
		}
		ids = append(ids, id)
	}

	snaps, err := svc.WaitFor(ctx, ids)
	if err != nil {
		return nil, transcribeOutput{}, err
	}

	out := transcribeOutput{
		Items: make([]transcriptItem, 0, len(snaps)),
		Model: m.cfg.ModelName(),
	}
	var joined strings.Builder
	for _, snap := range snaps {
		item := transcriptItem{
			Filename:   snap.Filename,
			Text:       snap.Text,
			Language:   snap.Language,
			DurationMS: snap.DurationMS,
			ElapsedMS:  snap.ElapsedMS,
			Error:      snap.Err,
		}
		out.Items = append(out.Items, item)
		if snap.Text != "" {
			if joined.Len() > 0 {
				joined.WriteString("\n\n")
			}
			joined.WriteString(snap.Text)
		}
	}
	out.Text = joined.String()

	// El texto también va como contenido plano: un agente que no lea la salida
	// estructurada igual recibe la transcripción.
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: out.Text}},
	}, out, nil
}

func (m *mcpServer) infoTool(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, infoOutput, error) {
	out := infoOutput{
		Model:    m.cfg.ModelName(),
		Models:   availableModels(m.cfg.ModelPath),
		Language: m.cfg.Language,
		Workers:  m.cfg.Workers,
		Threads:  m.cfg.Threads,
		Formats:  m.svc.Formats(),
		Root:     m.root,
		Prompt:   m.cfg.Prompt,
	}
	return nil, out, nil
}

// resolve valida la ruta pedida y la mantiene dentro de root si hay uno.
func (m *mcpServer) resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("falta la ruta del audio")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if m.root != "" {
		rel, err := filepath.Rel(m.root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s está fuera de %s, que es la única carpeta permitida", abs, m.root)
		}
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("no se puede leer %s: %w", abs, err)
	}
	return abs, nil
}

// availableModels lista los .bin que hay junto al modelo configurado, para que
// el agente sepa qué puede pedir sin adivinar.
func availableModels(modelPath string) []string {
	entries, err := os.ReadDir(filepath.Dir(modelPath))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bin") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// --- comando ------------------------------------------------------------------

func cmdMCP(ctx context.Context, args []string) error {
	cfg := config.Default()
	cfg.ApplyEnv()

	fset := flag.NewFlagSet("mcp", flag.ContinueOnError)
	var formats, root string
	bindCommon(fset, &cfg, &formats)
	fset.StringVar(&root, "root", os.Getenv("VOZGO_MCP_ROOT"), "limita al agente a esta carpeta (vacío = sin límite)")
	if err := fset.Parse(args); err != nil {
		return err
	}
	cfg.Formats = config.SplitFormats(formats)
	if err := cfg.LoadPrompt(); err != nil {
		return err
	}

	svc, err := transcribe.New(cfg)
	if err != nil {
		return err
	}
	cfg = svc.Config()
	svc.Start(ctx)

	if root != "" {
		if root, err = filepath.Abs(root); err != nil {
			return err
		}
	}
	m := &mcpServer{svc: svc, cfg: cfg, root: root}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "vozgo",
		Version: version,
		Title:   "vozgo — notas de voz a texto, en local",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "transcribe",
		Description: "Transcribe a texto un archivo de audio o todos los audios de una carpeta, " +
			"en la máquina local y sin enviar nada a internet. Sirve para notas de voz de " +
			"WhatsApp (.ogg/Opus) y cualquier formato que ffmpeg pueda decodificar.",
	}, m.transcribeTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "info",
		Description: "Devuelve el modelo en uso, los modelos disponibles y los límites de la instalación.",
	}, m.infoTool)

	// El log va a stderr: stdout es el canal del protocolo.
	fmt.Fprintf(os.Stderr, "vozgo mcp: modelo %s, idioma %s, %d worker(s)\n",
		cfg.ModelName(), cfg.Language, cfg.Workers)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() != nil {
			return nil // el cliente cerró la conexión
		}
		return err
	}
	return nil
}
