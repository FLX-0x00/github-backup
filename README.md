# GitHub Backup für Synology DSM

Natives DSM-Paket (`.spk`) für wöchentliche GitHub-Mirror-Backups – mit **DSM-App-Fenster** direkt im DSM-Webinterface. **Kein eigener Port, kein separater Webserver, kein Docker, kein externes Git.**

Der Git-Client ist als **go-git** fest im Go-Binary enthalten. Es wird **kein Git Server**, kein `git`, kein `git-lfs` und keine sonstige Laufzeit-Abhängigkeit benötigt.

Die Bedienung erfolgt vollständig über die GUI:

- Übersicht mit Status, laufendem Backup, letztem/nächstem Lauf und Speicherstatistik
- Verbindungen pro Konto/Org anlegen, bearbeiten, löschen, aktivieren/deaktivieren
- Token hinterlegen, ersetzen und **testen**
- Repositories auswählen (einzeln oder „alle inkl. zukünftiger“)
- Backup jetzt starten und laufendes Backup abbrechen
- Logs direkt in der GUI ansehen
- Speicherort, Zeitplan und Optionen nachträglich ändern

---

## 1. Welches SPK?

| Datei | NAS-Architektur | Typisch |
|---|---|---|
| `github-backup-1.0.7-x86_64.spk` | `x86_64` | Intel/AMD-DS, Virtual DSM |
| `github-backup-1.0.7-armv8.spk` | `armv8` | ARM 64-bit (rtd1296, armada37xx, rtd1619) |
| `github-backup-1.0.7-armada38x.spk` | `armada38x` | **DS216j**, DS216, DS416j u. a. (ARMv7 32-bit) |
| `github-backup-1.0.7-armv7.spk` | `armv7` | Alpine/Alpine4k |

Für die **DS216j mit DSM 7.0.1** ist `github-backup-1.0.7-armada38x.spk` die richtige Datei.

---

## 2. Installation

1. Package Center → **Manual Install** → passende `.spk` wählen.
2. Bei „unbekannter Herausgeber“: Package Center → Settings → General → Trust Level auf **Any Publisher**.
3. Im Installationsdialog ausfüllen:
   - **Token**: classic PAT mit `repo` + `read:org` (Default) oder fine-grained Token.
   - **Token-Typ**: classic / fine-grained User / fine-grained Org.
   - **Owner/Org**: nur bei fine-grained Org nötig.
   - **Speicherort**: z. B. `/volume1/backup/github`. Das Feld ist editierbar.
   - **Wochentag/Uhrzeit**: Default Sonntag 03:00.
   - **Optionen**: Wikis, Prune, Benachrichtigung (best effort).
   - **Repo-Auswahl**: leer = alle inkl. zukünftiger Repos; sonst kommagetrennte Liste.
4. Nach der Installation startet der Dienst (falls „nach Installation ausführen“ gewählt) und führt den ersten Lauf aus.

### App öffnen

Nach der Installation erscheint **GitHub Backup** im DSM-Hauptmenü. Darüber öffnet sich das App-Fenster. Die UI liegt im normalen DSM-Webinterface (Port 5000/5001) – **kein eigener Port, kein separater Webserver**.

### Wenn die App nicht öffnet

Die UI ist eine DSM-App (statische Dateien unter `/webman/3rdparty/github-backup/`), die API läuft als Paket-CGI (`ui/api.cgi`). Prüfen:

```sh
ls -l /usr/syno/synoman/webman/3rdparty/github-backup
ls -l /usr/syno/synoman/webman/3rdparty/github-backup/api.cgi
ls -l /tmp/github-backup-ui.sock
tail -50 /var/log/synoscgi.log
tail -50 /var/packages/github-backup/var/daemon.log
```

Bei `502` fehlt meist der Dienst/Socket; bei `404` fehlt der App-Link oder `ui/api.cgi` ist nicht ausführbar. Es gibt **keinen** nginx-`web-config`-Eintrag, keinen `webapi-desc`-Eintrag und keinen zusätzlichen TCP-Port.

Der interne Paketbenutzer `sc-github-backup` bekommt über die `data-share`-Resource automatisch Zugriff auf den Share **`backup`**; `backup/github` wird darin angelegt. Bei einem Ziel außerhalb von `backup` müssen die Rechte manuell gesetzt werden.

---

## 3. Token

### Classic (Default, für „alles“)

- Scopes: `repo`, `read:org`
- Link: <https://github.com/settings/tokens/new?scopes=repo,read:org&description=Synology%20GitHub%20Backup>
- Für Org-SAML: Token unter *Settings → Developer settings → Personal access tokens → Configure SSO* für jede Org autorisieren.
- Classic `repo` ist **read/write**; Token wie ein Passwort behandeln.

### Fine-grained

- Resource Owner: dein User **oder genau eine Org**.
- Permissions: `Contents: Read`, `Metadata: Read`.
- Repository access: „All repositories“ (oder passende Auswahl).
- Ein fine-grained Token deckt **nicht** mehrere Organizations gleichzeitig ab. Für weitere Orgs einfach in der GUI eine weitere Verbindung anlegen.
- Org muss den Token ggf. genehmigen.

---

## 4. GUI

### Übersicht

- Karten: Repositories, Wikis, Verbindungen, letzter/nächster Lauf
- Laufender Backup-Job mit Fortschritt, aktuellem Repository und Abbrechen-Button
- Verbindungs-Karten mit Status, letztem Lauf, Pfad und Schnellaktionen

### Verbindungen

- **Hinzufügen**: Classic, persönlich oder Org; Token wird beim Speichern geprüft.
- **Bearbeiten**: Name, Modus, eigener Pfad, aktiv/inaktiv; Token kann ersetzt werden.
- **Testen**: prüft Token und zeigt die Anzahl sichtbarer Repositories.
- **Löschen**: entfernt nur die Verbindung; vorhandene Mirror-Daten bleiben.
- **Repositories**: Auswahl-Dialog mit Suche, Privat/Fork/Archived-Markierungen und „Alle inkl. zukünftiger“.

### Logs

Jeder Lauf erzeugt eine Logdatei. In der GUI lassen sich die Läufe auflisten und direkt ansehen.

### Einstellungen

- Speicherort (absoluter Pfad)
- Wochentag, Stunde, Minute, Zeitplan aktiv/inaktiv
- Wikis, Prune, Benachrichtigung
- Parallelität und Log-Aufbewahrung

> Ein neuer Speicherort verschiebt bestehende Mirrors nicht. Neue/aktualisierte Repositories landen im neuen Pfad.

---

## 5. Wo liegt was?

```text
/volume1/backup/github/
├── repos/<owner>/<repo>.git
├── wikis/<owner>/<repo>.git
├── meta/
│   ├── manifest.tsv
│   └── last-run.txt
└── logs/
    └── run-*.log
```

Paket-Konfiguration und UI-Socket:

```text
/var/packages/github-backup/var/config.json
/tmp/github-backup-ui.sock
/var/packages/github-backup/var/daemon.log
```

Die UI-Dateien und die CGI-Bridge (`ui/api.cgi`) werden von DSM aus dem App-Ordner ausgeliefert. Die CGI-Bridge spricht den Hintergrunddienst über einen **Unix-Socket** an. Es gibt keinen TCP-Port für die UI.

---

## 6. Restore

Die Beispiele gelten für die Default-Classic-Verbindung. Bei fine-grained Verbindungen liegt der Mirror unter `<connection-path>/repos/<owner>/<repo>.git`; den Pfad zeigt die GUI in der Verbindungs-Karte.

```bash
git clone /volume1/backup/github/repos/OWNER/REPO.git /tmp/REPO
```

In ein neues, leeres GitHub-Repo pushen:

```bash
git -C /volume1/backup/github/repos/OWNER/REPO.git push \
  git@github.com:OWNER/REPO.git \
  'refs/heads/*:refs/heads/*' \
  'refs/tags/*:refs/tags/*'
```

Wiki:

```bash
git -C /volume1/backup/github/wikis/OWNER/REPO.git push --mirror \
  git@github.com:OWNER/REPO.wiki.git
```

---

## 7. Sicherheit

- Das Paket läuft als interner DSM-Paketbenutzer `sc-github-backup` – **kein root**.
- `config.json` ist `0600 sc-github-backup`; das Token ist nur für den Paketbenutzer/root lesbar.
- UI-Zugriff nur für DSM-Administratoren; die API prüft die DSM-Session und die Administratorengruppe.
- Kein eigener Port, keine Expo­sition der UI nach außen über einen zusätzlichen Dienst.
- Token wie ein Passwort behandeln, Ablaufdatum setzen, bei Leak widerrufen.
- Zusätzlich **Hyper Backup** auf externe Platte/Cloud einrichten (3-2-1).
- Restore einmal testen.

---

## 8. Technik

- Go-Binary, statisch, mit **go-git** (kein externes Git)
- bare Mirrors, alle Ref-/Branch-/Tag-Refs
- optional Wikis
- wöchentlicher Scheduler im Dienst
- UI als DSM-App-Fenster (`dsmuidir`), API als Paket-CGI (`ui/api.cgi`), **kein eigener Port**
- `data-share` für `backup`

---

## 9. Build

Voraussetzungen: Go 1.21+, `tar`, `md5sum`.

```bash
cd ghbackup
./build.sh
# -> dist/github-backup-1.0.7-{x86_64,armv8,armv7,armada38x}.spk
```

CI: `go vet`, `go test`, Go-Format, Shell-Syntax, Wizard-JSON und SPK-Build.

---

## 10. Grenzen

- Kein Issues-/PR-/Release-Backup – nur Git-Daten und Wikis.
- Keine Git-LFS-Objekte (bewusst, um keine externe Abhängigkeit einzuführen).
- Submodule werden nicht rekursiv gesichert.
- PR-Refs (`refs/pull/*`) können den Mirror größer machen.
- Prune ist standardmäßig aus; gelöschte Refs bleiben im Backup.
- Unsigned SPK: DSM verlangt ggf. „Any Publisher“.
- Die echte Installation auf DSM 7.0.1/DS216j muss auf dem Zielgerät verifiziert werden.

---

## 11. Optional: CLI

Die GUI ist der vorgesehene Weg. Für Automation/Notfälle existiert weiterhin das Binary unter `/var/packages/github-backup/target/bin/github-backup` (`config`, `set-token`, `select`, `run`, `list`, `test`). Die Config liegt unter `/var/packages/github-backup/var/config.json`.
