import * as path from 'path';
import * as fs from 'fs';
import { workspace, ExtensionContext } from 'vscode';

import {
    LanguageClient,
    LanguageClientOptions,
    ServerOptions,
    TransportKind,
    Executable
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
        path.join('../server', serverExeName)
    );

	console.debug("Start message");

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
            args: ['--debug'],
            options: {}
        }
    };

    // Options to control LSP server
    const clientOptions: LanguageClientOptions = {
        // Only works for plaintext
        documentSelector: [{ scheme: 'file', language: 'plaintext' }],
        synchronize: {
            // Watch '.clientrc' files in the workspace
            fileEvents: workspace.createFileSystemWatcher('**/.clientrc')
        }
    };

    // Create and start LSP client
    client = new LanguageClient(
        'languageServerExample',
        'Language Server Example',
        serverOptions,
        clientOptions
    );

    // Start the client, this will also start the server
    client.start();
}

export function deactivate(): Thenable<void> | undefined {
    if (!client) {
        return undefined;
    }
    return client.stop();
}
