package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	dmp "github.com/sergi/go-diff/diffmatchpatch"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

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
		switch params.Command {
		case "comment.add":
			if len(params.Arguments) != 2 {
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
			// Add comment function
			err := h.addComment(ctx, uri, rng)
			if err != nil {
				return reply(ctx, nil, err)
			}
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
	commentsFile := filepath.Join("comments", filepath.Base(filePath)+".json")
	data, err := os.ReadFile(commentsFile)
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

func applyPatchAndGetPositions(originalText string, patchText string) ([]int, error) {
	dmp := dmp.New()

	patches, err := dmp.PatchFromText(patchText)
	if err != nil {
		return nil, err
	}

	// Apply patch
	_, results := dmp.PatchApply(patches, originalText)
	if len(results) == 0 {
		return nil, fmt.Errorf("patch could not be applied")
	}

	// Find where the patch was applied
	var positions []int
	for _, p := range patches {
		startLine := p.Start1
		positions = append(positions, startLine)
	}

	return positions, nil
}

func (h *handler) publishDiagnostics(ctx context.Context, uri protocol.DocumentURI) {
	filePath := uriToPath(uri)

	// Load file content
	currentContentBytes, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("Error while reading the file %s: %v", filePath, err)
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
	commitPresent, err := isCommitInCurrentBranch(commentFile.Commit)
	if err != nil {
		log.Printf("Error while checking commit: %v", err)
		return
	}
	if !commitPresent {
		log.Printf("Commit %s is not on current branch. No comment will be displayed.", commentFile.Commit)
		return
	}

	var diagnostics []protocol.Diagnostic
	for _, patch := range commentFile.Patches {
		positions, err := applyPatchAndGetPositions(currentContent, patch.Patch)
		if err != nil {
			log.Printf("Error while applying the patch: %v", err)
			continue
		}

		for _, lineNumber := range positions {
			diagnostic := protocol.Diagnostic{
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(lineNumber), Character: 0},
					End:   protocol.Position{Line: uint32(lineNumber), Character: 100},
				},
				Severity: protocol.DiagnosticSeverityHint,
				Message:  patch.Message,
			}
			diagnostics = append(diagnostics, diagnostic)
		}
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
	parsed, _ := url.Parse(string(uri))
	return filepath.FromSlash(parsed.Path)
}

func getUserRepoDir(filePath string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = filepath.Dir(filePath)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not get the git repository from user folder : %v", err)
	}
	repoDir := strings.TrimSpace(string(output))
	return repoDir, nil
}

func (h *handler) addComment(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) error {
	// Demander le texte du commentaire
	params := map[string]interface{}{
		"prompt": "Write new comment :",
	}
	var result string
	_, err := h.conn.Call(ctx, "window/showInputBox", params, &result)
	if err != nil {
		return err
	}

	if result == "" {
		return fmt.Errorf("comment canceled or empty")
	}

	// Generate patch
	err = generateAndSaveCommentPatch(uri, rng, result)
	if err != nil {
		return err
	}

	// Update comments display
	h.publishDiagnostics(ctx, uri)

	return nil
}

func generateAndSaveCommentPatch(uri protocol.DocumentURI, rng protocol.Range, commentText string) error {
	filePath := uriToPath(uri)
	userRepoDir, err := getUserRepoDir(filePath)
	if err != nil {
		return err
	}

	// Get file content
	currentContentBytes, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("error while reading file %s: %v", filePath, err)
	}
	currentContent := string(currentContentBytes)

	// Get relative path of the file from repository root
	relativePath, err := filepath.Rel(userRepoDir, filePath)
	if err != nil {
		return fmt.Errorf("error while getting relative path : %v", err)
	}

	// Get file content for current commit
	cmd := exec.Command("git", "show", "HEAD:"+relativePath)
	cmd.Dir = userRepoDir
	originalContentBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("error while retrieving current commit: %v", err)
	}
	originalContent := string(originalContentBytes)

	// Get current commit hash
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = userRepoDir
	commitBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("error while retrieving current commit: %v", err)
	}
	commitHash := strings.TrimSpace(string(commitBytes))

	// Extract selected text
	//	lines := strings.Split(currentContent, "\n")
	//	startLine := int(rng.Start.Line)
	//	endLine := int(rng.End.Line)
	//	if endLine >= len(lines) {
	//		endLine = len(lines) - 1
	//	}
	//	selectedText := strings.Join(lines[startLine:endLine+1], "\n")

	// Generate diff between original file content and actual file content
	dmp := dmp.New()
	patches := dmp.PatchMake(originalContent, currentContent)
	patchText := dmp.PatchToText(patches)

	// Load or create comment file
	commentFilePath := filepath.Join("comments", filepath.Base(filePath)+".json")
	var commentFile CommentFile
	if _, err := os.Stat(commentFilePath); os.IsNotExist(err) {
		// If the file does not exist, create it
		commentFile = CommentFile{
			Commit:  commitHash,
			Patches: []Patch{},
		}
	} else {
		// Load existing file
		data, err := os.ReadFile(commentFilePath)
		if err != nil {
			return fmt.Errorf("error while reading comment file: %v", err)
		}
		err = json.Unmarshal(data, &commentFile)
		if err != nil {
			return fmt.Errorf("error while parsing comment file: %v", err)
		}
	}

	// Check if commit has changed
	if commentFile.Commit != commitHash {
		// If commit has changed
		commentFile.Commit = commitHash
		commentFile.Patches = []Patch{}
	}

	// Add new comment
	newPatch := Patch{
		Message: commentText,
		Patch:   patchText,
	}
	commentFile.Patches = append(commentFile.Patches, newPatch)

	// Save comment file
	data, err := json.MarshalIndent(commentFile, "", "  ")
	if err != nil {
		return fmt.Errorf("error while serializing comment file: %v", err)
	}
	err = os.WriteFile(commentFilePath, data, 0644)
	if err != nil {
		return fmt.Errorf("error while writing comment file: %v", err)
	}

	// Update comment repository
	err = updateCommentsRepoAfterChange()
	if err != nil {
		return fmt.Errorf("error while updating comment repository: %v", err)
	}

	return nil
}

func updateCommentsRepo() error {
	if _, err := os.Stat("comments"); os.IsNotExist(err) {
		// Clone repository
		cmd := exec.Command("git", "clone", "https://github.com/paulbaron/TestLSPComments.git", "comments")
		return cmd.Run()
	} else {
		// Update repository
		cmd := exec.Command("git", "-C", "comments", "pull")
		return cmd.Run()
	}
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
