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


    // Nom de base de l'exécutable
    let serverExeName = 'separate_comments';

    // Ajouter l'extension '.exe' sur Windows
    if (process.platform === 'win32') {
        serverExeName += '.exe';
    }

    // Chemin vers l'exécutable du serveur Go
    const serverExecutable = context.asAbsolutePath(
        path.join('../server', serverExeName)
    );

	console.debug("Start message");

    // Vérifier si l'exécutable existe
    if (!fs.existsSync(serverExecutable)) {
        console.error(`L'exécutable du serveur LSP n'existe pas : ${serverExecutable}`);
        // Vous pouvez afficher un message à l'utilisateur ou gérer l'erreur comme vous le souhaitez
        return;
    }

    // Définir les options du serveur en tant qu'exécutable
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

    // Options pour contrôler le client LSP
    const clientOptions: LanguageClientOptions = {
        // Enregistrez le serveur pour les documents texte
        documentSelector: [{ scheme: 'file', language: 'plaintext' }],
        synchronize: {
            // Surveillez les fichiers '.clientrc' dans le workspace
            fileEvents: workspace.createFileSystemWatcher('**/.clientrc')
        }
    };

    // Créer le client LSP et démarrer le client.
    client = new LanguageClient(
        'languageServerExample',
        'Language Server Example',
        serverOptions,
        clientOptions
    );

    // Démarrer le client. Cela lancera également le serveur
    client.start();
}

export function deactivate(): Thenable<void> | undefined {
    if (!client) {
        return undefined;
    }
    return client.stop();
}
