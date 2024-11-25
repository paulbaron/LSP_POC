import * as path from 'path';
import * as fs from 'fs';
import { workspace, ExtensionContext } from 'vscode';
import * as vscode from 'vscode';
import { Trace } from 'vscode-jsonrpc';

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

    console.debug(`Server exe : ${serverExeName}`);

    // Path to exe
    const serverExecutable = context.asAbsolutePath(
        path.join('server', serverExeName)
    );

    console.debug(`Path to exe : ${serverExecutable}`);

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
    // Start the client, this will also start the server
    console.debug(`start client and server`);
    client.start();
    client.setTrace(Trace.Verbose);
}

export function deactivate(): Thenable<void> | undefined {
    console.debug(`deactivate called`);
    if (!client) {
        return undefined;
    }
    return client.stop();
}
