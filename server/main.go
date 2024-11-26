package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	dmp "github.com/sergi/go-diff/diffmatchpatch"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

var contextBefore int = 5 // Context before patch
var contextAfter int = 5  // Context after patch

type stdrwc struct{}

func (s stdrwc) Read(p []byte) (int, error) {
	return os.Stdin.Read(p)
}

func (s stdrwc) Write(p []byte) (int, error) {
	return os.Stdout.Write(p)
}

func (s stdrwc) Close() error {
	return nil
}

func main() {
	log.SetOutput(os.Stderr)
	log.Println("Start LSP server...")

	err := updateCommentsRepo()
	if err != nil {
		log.Fatalf("error while updating comments: %v", err)
	}

	stream := jsonrpc2.NewStream(stdrwc{})
	conn := jsonrpc2.NewConn(stream)
	handler := handler{conn: conn}

	conn.Go(context.Background(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		return handler.handle(ctx, reply, req)
	})

	// Wait for end of connection
	<-conn.Done()

	if err := conn.Err(); err != nil {
		log.Fatalf("error while executing LSP server: %v", err)
	}
}

type handler struct {
	conn jsonrpc2.Conn
}

func (h *handler) handle(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	switch req.Method() {
	case "initialize":
		var params protocol.InitializeParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, err)
		}
		result := protocol.InitializeResult{
			Capabilities: protocol.ServerCapabilities{
				TextDocumentSync: protocol.TextDocumentSyncKindIncremental,
				CodeActionProvider: protocol.CodeActionOptions{
					CodeActionKinds: []protocol.CodeActionKind{
						"quickfix",
					},
				},
				ExecuteCommandProvider: &protocol.ExecuteCommandOptions{
					Commands: []string{"comment.add"},
				},
			},
		}
		return reply(ctx, result, nil)
	case "textDocument/didOpen":
		var params protocol.DidOpenTextDocumentParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, err)
		}
		h.publishDiagnostics(ctx, params.TextDocument.URI)
		return nil
	case "textDocument/didChange":
		var params protocol.DidChangeTextDocumentParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, err)
		}
		h.publishDiagnostics(ctx, params.TextDocument.URI)
		return nil
	case "textDocument/codeAction":
		var params protocol.CodeActionParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, err)
		}
		action := protocol.CodeAction{
			Title: "Add a new comment",
			Kind:  "quickfix",
			Command: &protocol.Command{
				Title:     "Add a new comment",
				Command:   "comment.add",
				Arguments: []interface{}{params.TextDocument.URI, params.Range},
			},
		}
		return reply(ctx, []protocol.CodeAction{action}, nil)
	case "workspace/executeCommand":
		var params protocol.ExecuteCommandParams
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, err)
		}
		log.Printf("Execute command %s with %d arguments", params.Command, len(params.Arguments))
		switch params.Command {
		case "comment.add":
			if len(params.Arguments) != 3 {
				return reply(ctx, nil, fmt.Errorf("invalid arguments count"))
			}
			uriStr, ok := params.Arguments[0].(string)
			if !ok {
				return reply(ctx, nil, fmt.Errorf("invalid argument type for URI"))
			}
			uri := protocol.DocumentURI(uriStr)
			rangeMap, ok := params.Arguments[1].(map[string]interface{})
			if !ok {
				return reply(ctx, nil, fmt.Errorf("invalid argument type for range"))
			}
			var rng protocol.Range
			rangeData, _ := json.Marshal(rangeMap)
			json.Unmarshal(rangeData, &rng)
			contentBody, ok := params.Arguments[2].(string)
			if !ok {
				return reply(ctx, nil, fmt.Errorf("invalid argument type for contentBody"))
			}
			// Add comment function
			err := h.addComment(ctx, uri, rng, contentBody)
			if err != nil {
				return reply(ctx, nil, err)
			}
			h.publishDiagnostics(ctx, uri)
			return reply(ctx, nil, nil)
		default:
			return reply(ctx, nil, fmt.Errorf("unrecognised command"))
		}
	default:
		return reply(ctx, nil, fmt.Errorf("method is not handled : %s", req.Method()))
	}
}

type CommentFile struct {
	Commit  string  `json:"commit"`
	Patches []Patch `json:"patches"`
}

type Patch struct {
	Message string `json:"message"`
	Patch   string `json:"patch"`
}

func loadCommentFile(filePath string) (*CommentFile, error) {
	commentFilePath, _, err := getCommentFilePath(filePath)
	if err != nil {
		return nil, err
	}
	log.Printf("Load comment file : %s", commentFilePath)
	data, err := os.ReadFile(commentFilePath)
	if err != nil {
		return nil, err
	}
	var commentFile CommentFile
	err = json.Unmarshal(data, &commentFile)
	if err != nil {
		return nil, err
	}
	return &commentFile, nil
}

func isCommitInCurrentBranch(commit string) (bool, error) {
	cmd := exec.Command("git", "branch", "--contains", commit)
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	branches := strings.TrimSpace(string(output))
	return branches != "", nil
}

func applyPatchAndGetPositions(originalText string, patchText string) (protocol.Range, error) {
	dmp := dmp.New()

	// Convertir le texte du patch en objets Patch
	patches, err := dmp.PatchFromText(patchText)
	if err != nil {
		return protocol.Range{}, err
	}

	// Appliquer le patch pour obtenir le nouveau texte et les résultats
	_, results := dmp.PatchApply(patches, originalText)
	if len(results) == 0 {
		return protocol.Range{}, fmt.Errorf("could not apply current patch")
	}

	for idx, p := range patches {
		log.Printf("patch %d : start1: %d, length1: %d, start2: %d, length2: %d", idx, p.Start1, p.Length1, p.Start2, p.Length2)
	}

	// Trouver les positions où les patches ont été appliqués
	patchLine := patches[0].Start1
	patchLength := patches[0].Length1

	start := protocol.Position{Line: uint32(patchLine + contextBefore), Character: 0}
	end := protocol.Position{Line: uint32(patchLine + patchLength - contextAfter), Character: 0}
	log.Printf("range is from line %d to line %d", start.Line, end.Line)
	return protocol.Range{Start: start, End: end}, nil
}

func (h *handler) publishDiagnostics(ctx context.Context, uri protocol.DocumentURI) {
	log.Printf("publishDiagnostics: Start function")
	filePath := uriToPath(uri)
	// Load file content
	currentContentBytes, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("publishDiagnostics: error while reading file %s: %v", filePath, err)
		return
	}
	currentContent := string(currentContentBytes)

	// Load comments and patches
	commentFile, err := loadCommentFile(filePath)
	if err != nil {
		log.Printf("No comments found for %s: %v", filePath, err)
		return
	}

	// Check if commit is on current branch
	if commentFile.Commit != "" {
		commitPresent, err := isCommitInCurrentBranch(commentFile.Commit)
		if err != nil {
			log.Printf("Error while checking commit: %v", err)
			return
		}
		if !commitPresent {
			log.Printf("Commit %s is not on current branch. No comment will be displayed.", commentFile.Commit)
			return
		}
	}

	var diagnostics []protocol.Diagnostic
	for _, patch := range commentFile.Patches {
		position, err := applyPatchAndGetPositions(currentContent, patch.Patch)
		if err != nil {
			log.Printf("Error while applying the patch: %v", err)
			continue
		}
		diagnostic := protocol.Diagnostic{
			Range:    position,
			Severity: protocol.DiagnosticSeverityHint,
			Message:  patch.Message,
		}
		diagnostics = append(diagnostics, diagnostic)
	}

	// Envoyer les diagnostics à l'éditeur
	params := protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diagnostics,
	}

	// Envoyer la notification
	h.conn.Notify(ctx, "textDocument/publishDiagnostics", params)
}

func uriToPath(uri protocol.DocumentURI) string {
	parsed, err := url.Parse(string(uri))
	if err != nil {
		log.Printf("Failed to parse URI: %v", err)
		return ""
	}
	path := parsed.Path
	// Handle Windows paths
	if runtime.GOOS == "windows" {
		// Remove leading '/' from the path if present
		path = strings.TrimPrefix(path, "/")
		// Replace forward slashes with backslashes
		path = filepath.FromSlash(path)
	} else {
		// For Unix-like systems, use FromSlash to handle any backslashes
		path = filepath.FromSlash(path)
	}
	// Decode URL-encoded characters
	path, err = url.PathUnescape(path)
	if err != nil {
		log.Printf("Failed to unescape path: %v", err)
		return ""
	}
	return path
}

func getUserRepoDir(filePath string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = filepath.Dir(filePath)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	repoDir := strings.TrimSpace(string(output))
	return repoDir
}

func (h *handler) addComment(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range, commentBody string) error {
	// Generate patch
	err := generateAndSaveCommentPatch(uri, rng, commentBody)
	if err != nil {
		return err
	}
	// Update comments display
	h.publishDiagnostics(ctx, uri)
	return nil
}

// Returns:
// - The current comment file path
// - The root of the current git repository (if there is one)
func getCommentFilePath(filePath string) (string, string, error) {
	userRepoDir := getUserRepoDir(filePath)
	if userRepoDir != "" {
		// If there is a git setup, we can retrieve the commitHash and the relative path
		// File relative path
		gitRelativePath, err := filepath.Rel(userRepoDir, filePath)
		if err != nil {
			return "", userRepoDir, fmt.Errorf("error while getting relative path : %v", err)
		}
		commentFilePath := filepath.Join(userRepoDir, "comments", gitRelativePath+".json")
		return commentFilePath, userRepoDir, nil
	} else {
		return filePath + ".json", "", nil
	}
}

func generateAndSaveCommentPatch(uri protocol.DocumentURI, rng protocol.Range, commentText string) error {
	filePath := uriToPath(uri)
	// Current file content
	currentContentBytes, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("error while reading file %s: %v", filePath, err)
	}
	currentContent := string(currentContentBytes)
	commentFilePath, userRepoDir, err := getCommentFilePath(filePath)
	if err != nil {
		return err
	}
	var commitHash = ""
	if userRepoDir != "" {
		// Current commit hash
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = userRepoDir
		commitBytes, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("erreur lors de la récupération du commit courant: %v", err)
		}
		commitHash = strings.TrimSpace(string(commitBytes))
	}

	// Extract current text
	lines := strings.Split(currentContent, "\n")
	linesCount := len(lines)
	// Get selected text
	startLine := int(rng.Start.Line)
	endLine := int(rng.End.Line)
	if startLine >= linesCount {
		startLine = linesCount - 1
	}
	if endLine >= linesCount {
		endLine = linesCount - 1
	}
	// Get context lines
	contextStart := startLine - contextBefore
	if contextStart < 0 {
		contextStart = 0
	}
	contextEnd := endLine + contextAfter + 1
	if contextEnd > linesCount {
		contextEnd = linesCount
	}
	// Generate context text
	patchText := fmt.Sprintf("@@ -%d,%d +%d,%d @@\n",
		contextStart+1,
		contextEnd-contextStart,
		contextStart+1,
		contextEnd-contextStart)
	for i := contextStart; i < startLine; i++ {
		patchText += " " + lines[i] + "\n"
	}
	for i := startLine; i <= endLine; i++ {
		patchText += "-" + lines[i] + "\n"
	}
	for i := startLine; i <= endLine; i++ {
		patchText += "+" + lines[i] + "\n"
	}
	for i := endLine + 1; i < contextEnd; i++ {
		patchText += " " + lines[i] + "\n"
	}

	// Load or create comment file
	var commentFile CommentFile
	if _, err := os.Stat(commentFilePath); os.IsNotExist(err) {
		// If the file does not exist, create it
		commentFile = CommentFile{
			Commit:  commitHash,
			Patches: []Patch{},
		}
	} else {
		// Load the existing file
		data, err := os.ReadFile(commentFilePath)
		if err != nil {
			return fmt.Errorf("error while reading comment file: %v", err)
		}
		err = json.Unmarshal(data, &commentFile)
		if err != nil {
			return fmt.Errorf("error while parsing comment file: %v", err)
		}
	}

	// Add the new comment
	newPatch := Patch{
		Message: commentText,
		Patch:   patchText,
	}
	commentFile.Patches = append(commentFile.Patches, newPatch)

	// Save the comment file
	data, err := json.MarshalIndent(commentFile, "", "  ")
	if err != nil {
		return fmt.Errorf("error while serializing comment file: %v", err)
	}
	err = os.MkdirAll(filepath.Dir(commentFilePath), fs.ModePerm)
	if err != nil {
		return fmt.Errorf("error while creating folders: %v", err)
	}
	err = os.WriteFile(commentFilePath, data, 0644)
	if err != nil {
		return fmt.Errorf("error while writing comment file: %v", err)
	}

	// Update the comments repository
	err = updateCommentsRepoAfterChange()
	if err != nil {
		return fmt.Errorf("error while updating comments repository: %v", err)
	}
	return nil
}

func updateCommentsRepo() error {
	if _, err := os.Stat("comments"); os.IsNotExist(err) {
		// Clone repository
		os.Mkdir("comments", os.ModeDir)
		//		cmd := exec.Command("git", "clone", "https://github.com/paulbaron/TestLSPComments.git", "comments")
		//		return cmd.Run()
	} else {
		// Update repository
		//		cmd := exec.Command("git", "-C", "comments", "pull")
		//		return cmd.Run()
	}
	return nil
}

func updateCommentsRepoAfterChange() error {
	// Not working RN
	/*
		cmd := exec.Command("git", "-C", "comments", "add", ".")
		err := cmd.Run()
		if err != nil {
			return fmt.Errorf("error while adding files to git: %v", err)
		}

		cmd = exec.Command("git", "-C", "comments", "commit", "-m", "Mise à jour des commentaires")
		err = cmd.Run()
		if err != nil {
			return fmt.Errorf("error on files commit: %v", err)
		}

		cmd = exec.Command("git", "-C", "comments", "push")
		err = cmd.Run()
		if err != nil {
			return fmt.Errorf("error while pushing new commit: %v", err)
		}
	*/
	return nil
}
