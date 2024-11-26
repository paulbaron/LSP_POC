import * as path from 'path';
import * as fs from 'fs';
import { workspace, ExtensionContext } from 'vscode';
import * as vscode from 'vscode';

import {
    LanguageClient,
    LanguageClientOptions,
    ServerOptions,
} from 'vscode-languageclient/node';

let client: LanguageClient;

export function activate(context: ExtensionContext) {
    // Server exe file name
    let serverExeName = 'separate_comments';

    // Add extension '.exe' for Windows
    if (process.platform === 'win32') {
        serverExeName += '.exe';
    }
    // Path to exe
    const serverExecutable = context.asAbsolutePath(
        path.join('server', serverExeName)
    );
    // Check if exe exists
    if (!fs.existsSync(serverExecutable)) {
        console.error(`Could not find LSP server exe : ${serverExecutable}`);
        return;
    }

    const serverOptions: ServerOptions = {
        run: {
            command: serverExecutable,
            args: [],
            options: {}
        },
        debug: {
            command: serverExecutable,
            args: [],
            options: {}
        },
    };

    const outputChannel = vscode.window.createOutputChannel('LSP Server');
    const traceOutputChannel = vscode.window.createOutputChannel('LSP Trace');

    // Options to control LSP server
    const clientOptions: LanguageClientOptions = {
        // Only works for plaintext
        documentSelector: [{ scheme: 'file', language: 'plaintext' }],
        synchronize: {
            // Watch '.clientrc' files in the workspace
            fileEvents: workspace.createFileSystemWatcher('**/.clientrc')
        },
        outputChannel: outputChannel,             // General logs
        traceOutputChannel: traceOutputChannel,   // Trace logs
    };

    // Create and start LSP client
    client = new LanguageClient(
        'languageServerExample',
        'Language Server Example',
        serverOptions,
        clientOptions
    );

	// Create a CommentController
	const commentController = vscode.comments.createCommentController(
		'comment-sample',
		'Comment API Sample'
	);
	context.subscriptions.push(commentController);
	// Provide Commenting Ranges
	commentController.commentingRangeProvider = {
		provideCommentingRanges: (document: vscode.TextDocument, _token: vscode.CancellationToken) => {
			const lineCount = document.lineCount;
			return [new vscode.Range(0, 0, lineCount - 1, 0)];
		}
	};

    // Command to handle comment validation
    context.subscriptions.push(
        vscode.commands.registerCommand('mywiki.createNote', async (reply: vscode.CommentReply) => {
            const commentBody = reply.text;
            if (!commentBody.trim()) {
                vscode.window.showErrorMessage('Comment cannot be empty.');
                return;
            }
            const uri = reply.thread.uri.toString();
            const range = {
                start: { line: reply.thread.range.start.line, character: reply.thread.range.start.character },
                end: { line: reply.thread.range.end.line, character: reply.thread.range.end.character },
            };
            // Call workspace/executeCommand to handle the comment
            await vscode.commands.executeCommand('comment.add', uri, range, commentBody);
            vscode.window.showInformationMessage('Command was sent.');
        })
    );

    // Command Palette entry to start a comment
    context.subscriptions.push(
        vscode.commands.registerCommand('comment-sample.startCommenting', () => {
            const activeEditor = vscode.window.activeTextEditor;
            if (!activeEditor) {
                vscode.window.showErrorMessage('No active editor.');
                return;
            }

            const range = activeEditor.selection;
            vscode.commands.executeCommand('comment-sample.startComment', activeEditor.document.uri, range);
        })
    );

    // Start the client, this will also start the server
    client.start();
 }

export function deactivate(): Thenable<void> | undefined {
    console.debug(`deactivate called`);
    if (!client) {
        return undefined;
    }
    return client.stop();
}
