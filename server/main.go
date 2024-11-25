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
	log.Println("Démarrage du serveur LSP...")

	// Mettre à jour le dépôt des commentaires
	err := updateCommentsRepo()
	if err != nil {
		log.Fatalf("erreur lors de la mise à jour du dépôt des commentaires: %v", err)
	}

	// Utiliser stdio pour la communication LSP
	stream := jsonrpc2.NewStream(stdrwc{})
	conn := jsonrpc2.NewConn(stream)
	handler := handler{conn: conn}

	// Démarrer le traitement des requêtes
	conn.Go(context.Background(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		return handler.handle(ctx, reply, req)
	})

	// Attendre la fin de la connexion
	<-conn.Done()

	// Vérifier s'il y a eu une erreur
	if err := conn.Err(); err != nil {
		log.Fatalf("Erreur lors de l'exécution du serveur LSP: %v", err)
	}
}

type handler struct {
	conn jsonrpc2.Conn
}

func (h *handler) handle(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	switch req.Method() {
	case "initialize":
		// Gérer l'initialisation
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
		// Proposer une action pour ajouter un commentaire
		action := protocol.CodeAction{
			Title: "Ajouter un commentaire",
			Kind:  "quickfix",
			Command: &protocol.Command{
				Title:     "Ajouter un commentaire",
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
				return reply(ctx, nil, fmt.Errorf("nombre d'arguments invalide"))
			}
			uriStr, ok := params.Arguments[0].(string)
			if !ok {
				return reply(ctx, nil, fmt.Errorf("type d'argument invalide pour l'URI"))
			}
			uri := protocol.DocumentURI(uriStr)
			rangeMap, ok := params.Arguments[1].(map[string]interface{})
			if !ok {
				return reply(ctx, nil, fmt.Errorf("type d'argument invalide pour la plage"))
			}
			var rng protocol.Range
			rangeData, _ := json.Marshal(rangeMap)
			json.Unmarshal(rangeData, &rng)
			// Appeler la fonction pour ajouter le commentaire
			err := h.addComment(ctx, uri, rng)
			if err != nil {
				return reply(ctx, nil, err)
			}
			return reply(ctx, nil, nil)
		default:
			return reply(ctx, nil, fmt.Errorf("commande non reconnue"))
		}
	default:
		return reply(ctx, nil, fmt.Errorf("méthode non prise en charge : %s", req.Method()))
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

	// Appliquer le patch pour obtenir le nouveau texte
	_, results := dmp.PatchApply(patches, originalText)
	if len(results) == 0 {
		return nil, fmt.Errorf("le patch n'a pas pu être appliqué")
	}

	// Trouver les positions où les patchs ont été appliqués
	var positions []int
	for _, p := range patches {
		startLine := p.Start1
		positions = append(positions, startLine)
	}

	return positions, nil
}

func (h *handler) publishDiagnostics(ctx context.Context, uri protocol.DocumentURI) {
	filePath := uriToPath(uri)

	// Charger le contenu actuel du fichier
	currentContentBytes, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("Erreur lors de la lecture du fichier %s: %v", filePath, err)
		return
	}
	currentContent := string(currentContentBytes)

	// Charger les commentaires et les patchs
	commentFile, err := loadCommentFile(filePath)
	if err != nil {
		log.Printf("Aucun commentaire pour %s: %v", filePath, err)
		return
	}

	// Vérifier si le commit est présent dans la branche courante
	commitPresent, err := isCommitInCurrentBranch(commentFile.Commit)
	if err != nil {
		log.Printf("Erreur lors de la vérification du commit: %v", err)
		return
	}
	if !commitPresent {
		log.Printf("Le commit %s n'est pas présent dans la branche courante. Aucun commentaire n'est affiché.", commentFile.Commit)
		return
	}

	var diagnostics []protocol.Diagnostic
	for _, patch := range commentFile.Patches {
		positions, err := applyPatchAndGetPositions(currentContent, patch.Patch)
		if err != nil {
			log.Printf("Erreur lors de l'application du patch: %v", err)
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
		return "", fmt.Errorf("erreur lors de la récupération du répertoire du dépôt Git de l'utilisateur : %v", err)
	}
	repoDir := strings.TrimSpace(string(output))
	return repoDir, nil
}

func (h *handler) addComment(ctx context.Context, uri protocol.DocumentURI, rng protocol.Range) error {
	// Demander le texte du commentaire
	params := map[string]interface{}{
		"prompt": "Entrez le commentaire :",
	}
	var result string
	_, err := h.conn.Call(ctx, "window/showInputBox", params, &result)
	if err != nil {
		return err
	}

	if result == "" {
		return fmt.Errorf("commentaire annulé ou vide")
	}

	// Générer le patch
	err = generateAndSaveCommentPatch(uri, rng, result)
	if err != nil {
		return err
	}

	// Publier les diagnostics pour mettre à jour l'affichage des commentaires
	h.publishDiagnostics(ctx, uri)

	return nil
}

func generateAndSaveCommentPatch(uri protocol.DocumentURI, rng protocol.Range, commentText string) error {
	filePath := uriToPath(uri)
	userRepoDir, err := getUserRepoDir(filePath)
	if err != nil {
		return err
	}

	// Obtenir le contenu actuel du fichier
	currentContentBytes, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("erreur lors de la lecture du fichier %s: %v", filePath, err)
	}
	currentContent := string(currentContentBytes)

	// Obtenir le chemin relatif du fichier par rapport au dépôt
	relativePath, err := filepath.Rel(userRepoDir, filePath)
	if err != nil {
		return fmt.Errorf("erreur lors de la détermination du chemin relatif : %v", err)
	}

	// Obtenir le contenu du fichier au commit courant
	cmd := exec.Command("git", "show", "HEAD:"+relativePath)
	cmd.Dir = userRepoDir
	originalContentBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("erreur lors de la récupération du contenu au commit courant: %v", err)
	}
	originalContent := string(originalContentBytes)

	// Obtenir le hash du commit courant
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = userRepoDir
	commitBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("erreur lors de la récupération du commit courant: %v", err)
	}
	commitHash := strings.TrimSpace(string(commitBytes))

	// Extraire le texte sélectionné
	//	lines := strings.Split(currentContent, "\n")
	//	startLine := int(rng.Start.Line)
	//	endLine := int(rng.End.Line)
	//	if endLine >= len(lines) {
	//		endLine = len(lines) - 1
	//	}
	//	selectedText := strings.Join(lines[startLine:endLine+1], "\n")

	// Générer le diff entre le contenu original et le contenu actuel
	dmp := dmp.New()
	patches := dmp.PatchMake(originalContent, currentContent)
	patchText := dmp.PatchToText(patches)

	// Charger ou créer le fichier de commentaires
	commentFilePath := filepath.Join("comments", filepath.Base(filePath)+".json")
	var commentFile CommentFile
	if _, err := os.Stat(commentFilePath); os.IsNotExist(err) {
		// Le fichier n'existe pas, créer un nouveau CommentFile
		commentFile = CommentFile{
			Commit:  commitHash,
			Patches: []Patch{},
		}
	} else {
		// Charger le fichier existant
		data, err := os.ReadFile(commentFilePath)
		if err != nil {
			return fmt.Errorf("erreur lors de la lecture du fichier de commentaires: %v", err)
		}
		err = json.Unmarshal(data, &commentFile)
		if err != nil {
			return fmt.Errorf("erreur lors du parsing du fichier de commentaires: %v", err)
		}
	}

	// Vérifier si le commit a changé
	if commentFile.Commit != commitHash {
		// Gérer le cas où le commit a changé
		commentFile.Commit = commitHash
		commentFile.Patches = []Patch{}
	}

	// Ajouter le nouveau commentaire
	newPatch := Patch{
		Message: commentText,
		Patch:   patchText,
	}
	commentFile.Patches = append(commentFile.Patches, newPatch)

	// Sauvegarder le fichier de commentaires
	data, err := json.MarshalIndent(commentFile, "", "  ")
	if err != nil {
		return fmt.Errorf("erreur lors de la sérialisation du fichier de commentaires: %v", err)
	}
	err = os.WriteFile(commentFilePath, data, 0644)
	if err != nil {
		return fmt.Errorf("erreur lors de l'écriture du fichier de commentaires: %v", err)
	}

	// Mettre à jour le dépôt des commentaires
	err = updateCommentsRepoAfterChange()
	if err != nil {
		return fmt.Errorf("erreur lors de la mise à jour du dépôt des commentaires: %v", err)
	}

	return nil
}

func updateCommentsRepo() error {
	if _, err := os.Stat("comments"); os.IsNotExist(err) {
		// Cloner le dépôt
		cmd := exec.Command("git", "clone", "https://github.com/paulbaron/TestLSPComments.git", "comments")
		return cmd.Run()
	} else {
		// Mettre à jour le dépôt
		cmd := exec.Command("git", "-C", "comments", "pull")
		return cmd.Run()
	}
}

func updateCommentsRepoAfterChange() error {
	cmd := exec.Command("git", "-C", "comments", "add", ".")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("erreur lors de l'ajout des fichiers: %v", err)
	}

	cmd = exec.Command("git", "-C", "comments", "commit", "-m", "Mise à jour des commentaires")
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("erreur lors du commit: %v", err)
	}

	cmd = exec.Command("git", "-C", "comments", "push")
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("erreur lors du push: %v", err)
	}

	return nil
}
