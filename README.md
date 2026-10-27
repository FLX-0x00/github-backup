# GitHub Backup für Synology DSM

Natives DSM-Paket (`.spk`) für wöchentliche GitHub-Mirror-Backups. **Kein Webserver, kein Web-UI, kein Docker.** Die Bedienung ist der native DSM-Installationsdialog, das Paket-Icon, optionale DSM-Benachrichtigungen (best effort) und – für spätere Änderungen – ein kleines CLI.

Enthalten:

- alle Repositories, auf die der Token Zugriff hat (eigene Repos inkl. Forks, Org-Repos, Outside-Collaborations bei classic)
- bare Git-Mirrors mit allen Branches/Tags/Refs
- optional Wikis
- optional Git-LFS
- wöchentlicher Zeitplan (Daemon im Paket)
- optionale DSM-Benachrichtigung nach jedem Lauf (best effort)
- Speicherort pro Installation; pro Owner/Org wird ein eigener Unterordner angelegt
- Repo-Auswahl optional schon im Installationsdialog, später per CLI

Nicht enthalten: Issues/PRs/Discussions/Releases, GitHub-Actions-Artifacts, Packages, Submodule (nur Verweise), Branch-Protection/Secrets/Webhooks.

---

## 1. Welches SPK?

| Datei | NAS-Architektur | Typisch |
|---|---|---|
| `github-backup-1.0.3-x86_64.spk` | `x86_64` | Intel/AMD-DS, Virtual DSM |
| `github-backup-1.0.3-armv8.spk` | `armv8` | ARM 64-bit (rtd1296, armada37xx, rtd1619) |
| `github-backup-1.0.3-armada38x.spk` | `armada38x` | **DS216j**, DS216, DS416j u. a. (ARMv7 32-bit) |
| `github-backup-1.0.3-armv7.spk` | `armv7` | Alpine/Alpine4k |

Prüfen per SSH:

```bash
uname -m
# x86_64 -> x86_64
# aarch64 -> armv8
# armv7l -> bei DS216j/armada38x: armada38x-SPK (nicht armv7)
```

Im Zweifel: DSM → Systemsteuerung → Info Center → CPU/Modell, oder die Synology-Kompatibilitätsliste „Package Arch“.

---

## 2. Installation

1. **Git Server** aus dem Synology Package Center installieren. Das Paket benötigt `git` von dort; ohne Git bricht die Installation mit einer klaren Meldung ab.
2. Package Center → **Manual Install** → passende `.spk` wählen.
3. Falls DSM „unbekannter Herausgeber“ meldet: Package Center → Settings → General → Trust Level auf **Any Publisher** (oder „Synology Inc. and trusted publishers“ entsprechend) stellen. Das Paket ist **nicht von Synology signiert**; das ist bei selbstgebauten SPKs normal.
4. Im Installationsdialog ausfüllen:
   - **Token**: classic PAT mit `repo` + `read:org` (Default) oder fine-grained Token.
   - **Token-Typ**: classic / fine-grained User / fine-grained Org.
   - **Owner/Org**: nur bei fine-grained Org nötig.
   - **Speicherort**: z. B. `/volume1/backup/github`.
   - **Wochentag/Uhrzeit**: Default Sonntag 03:00.
   - **Optionen**: Wikis, LFS, Prune, Benachrichtigung (best effort).
   - **Repo-Auswahl**: leer = **alle inkl. zukünftiger Repos**; sonst kommagetrennte `owner/repo`-Liste.
5. Nach der Installation startet der Dienst (falls „nach Installation starten“ gewählt) und führt den **ersten Lauf sofort** aus. Danach nach Zeitplan.

### Token

**Classic (Default, empfohlen für „alles“):**

- Scopes: `repo`, `read:org`
- Link: <https://github.com/settings/tokens/new?scopes=repo,read:org&description=Synology%20GitHub%20Backup>
- Für Org-SAML: Token unter *Settings → Developer settings → Personal access tokens → Configure SSO* für jede Org autorisieren.
- Classic `repo` ist **read/write**; Token wie ein Passwort behandeln.

**Fine-grained:**

- Resource Owner: dein User **oder genau eine Org**.
- Permissions: `Contents: Read`, `Metadata: Read`.
- Repository access: „All repositories“ (oder passende Auswahl).
- Ein fine-grained Token kann **nicht** mehrere Organizations gleichzeitig abdecken. Für weitere Orgs nach der Installation `add-connection` verwenden.
- Org muss den Token ggf. genehmigen.

---

## 3. Wo liegt was?

```text
/volume1/backup/github/                 # wizard_storage
├── repos/<owner>/<repo>.git            # bare Mirror
├── wikis/<owner>/<repo>.git            # optional
├── meta/
│   ├── manifest.tsv
│   └── last-run.txt
└── logs/
    └── run-YYYYmmdd-HHMMSS.log
```

Paket-Konfiguration:

```text
/var/packages/github-backup/var/config.json   # 0600, enthält Token
/var/packages/github-backup/var/daemon.log
```

Bei fine-grained Verbindungen liegt der Standardpfad unter `<storage>/<owner>/...`; classic nutzt `<storage>/repos/<owner>/...`, also pro Owner/Org einen eigenen Ordner.

---

## 4. Später verwalten (CLI)

Die CLI läuft als Paketbenutzer, damit Config/Logs nicht root gehören. Als Administrator:

```bash
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup --help
```

Config:

```bash
/var/packages/github-backup/var/config.json
```

### Repos auflisten

```bash
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup list \
  --config /var/packages/github-backup/var/config.json
```

### Auswahl ändern

```bash
# nur bestimmte Repos
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup select \
  --config /var/packages/github-backup/var/config.json \
  --repos "owner/repo1,owner/repo2"

# wieder alles inkl. neuer Repos
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup select \
  --config /var/packages/github-backup/var/config.json --all
```

### Weitere Org/User-Verbindung (fine-grained)

```bash
# Token als Paketbenutzer in die Paket-Var legen (600)
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  sh -c 'umask 077; cat > /var/packages/github-backup/var/org-token'
# Token einfügen, Ctrl-D

sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup add-connection \
  --config /var/packages/github-backup/var/config.json \
  --kind org \
  --owner DEINE-ORG \
  --token-file /var/packages/github-backup/var/org-token \
  --data-root /volume1/backup/github
```

### Sofort sichern

```bash
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup run \
  --config /var/packages/github-backup/var/config.json
```

### Verbindungen testen

```bash
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup test \
  --config /var/packages/github-backup/var/config.json
```

### Dienst steuern

```bash
synopkg stop github-backup && synopkg start github-backup
synopkg stop github-backup
synopkg start github-backup
```

Der Daemon liest die Config alle 30 Sekunden neu; Änderungen an Auswahl/Verbindungen greifen ohne Neustart.

---

## 5. Zeitplan

Der Daemon im Paket prüft alle 30 Sekunden, ob der wöchentliche Termin fällig ist. Nach einem Neustart/Stop wird ein verpasster Termin nachgeholt, sobald der Daemon wieder läuft. Der erste Lauf nach Installation erfolgt sofort.

- Paket muss **gestartet** sein.
- NAS-Hibernation kann den Daemon an der Ausführung hindern; für zuverlässige wöchentliche Backups Hibernation deaktivieren oder den Lauf manuell anstoßen.
- Zeitplan ändern:

```bash
sudo -u sc-github-backup env HOME=/var/packages/github-backup/var/home \
  /var/packages/github-backup/target/bin/github-backup schedule \
  --config /var/packages/github-backup/var/config.json \
  --weekday 0 --hour 3 --minute 0 --enable
```

`weekday`: 0=Sonntag … 6=Samstag. Der Daemon liest die Config alle 30 Sekunden neu; ein Neustart ist nicht nötig.

---

## 6. Restore

Die Beispiele gelten für die Default-Classic-Verbindung. Bei fine-grained Verbindungen liegt der Mirror unter `<connection-path>/repos/<owner>/<repo>.git`; den exakten Pfad findest du in `config.json` (`data_root`, `connections[].path`).

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

LFS-Objekte müssen separat übertragen werden (`git lfs push --all ...`).

---

## 7. Sicherheit

- Das Paket läuft komplett als interner DSM-Paketbenutzer `sc-github-backup` – **kein root**, auch nicht für die Installation.
- Beim ersten Start legt DSM über `data-share` den Share **`backup`** an (falls nicht vorhanden) und gibt `sc-github-backup` automatisch Lesen/Schreiben. Danach wird `backup/github` vom Paketbenutzer erstellt.
- Wenn du einen **anderen** Speicherort außerhalb des `backup`-Shares wählst, muss `sc-github-backup` dort manuell Lesen/Schreiben bekommen.
- `config.json` ist `0600 sc-github-backup` und enthält den Token im Klartext (nur Paketbenutzer/root lesbar). Kein Webserver, kein Port, keine Token-Übertragung über HTTP.
- Backup-Ordner: `0700 sc-github-backup` – private Repos sind nur für den Paketbenutzer (und root per SSH) lesbar.
- Token wie ein Passwort behandeln, Ablaufdatum setzen, bei Leak widerrufen.
- Zusätzlich **Hyper Backup** auf externe Platte/Cloud einrichten (3-2-1). Das NAS-Backup allein ist kein 3-2-1-Backup.
- Restore einmal testen.

---

## 8. Deinstallation

Package Center → Uninstall. Der Service wird gestoppt; **Backup-Daten bleiben** im gewählten Speicherort erhalten. `postuninst` entfernt die Paket-Config samt Token; Token danach ggf. in GitHub widerrufen.

---

## 9. Selbst bauen

Voraussetzungen: Go 1.21+, `tar`, `md5sum`. Die Icons liegen fertig im Repo.

```bash
cd ghbackup
./build.sh
# -> dist/github-backup-1.0.3-{x86_64,armv8,armv7,armada38x}.spk
```

Die Skripte unter `spk/scripts/` sind `/bin/sh`-kompatibel; das Go-Binary ist statisch (`CGO_ENABLED=0`).

---

## 10. Grenzen / bewusste Entscheidungen

- **Kein Web-UI.** Native DSM-Installation + CLI; kein Port, kein Webserver, keine zusätzliche Angriffsfläche.
- **Repo-Auswahl im Wizard** ist eine optionale kommagetrennte Liste. Für komfortable Auswahl nach der Installation dient `list`/`select` per SSH. Ein DSM-Desktop-App-Fenster wäre ein eigener Ausbauschritt.
- **Fine-grained = ein Owner pro Token.** Mehrere Orgs = mehrere Verbindungen (`add-connection`).
- **Kein Issues-/PR-/Release-Backup.** Nur Git-Daten/Wikis/LFS.
- **Submodule** werden nicht rekursiv gesichert.
- **PR-Refs** (`refs/pull/*`) können den Mirror größer machen; `git clone --mirror` nimmt sie mit, wenn GitHub sie bewirbt.
- **Prune** ist standardmäßig aus: gelöschte Branches bleiben im Backup. Im Wizard „Gelöschte Branches auch im Backup löschen“ aktiviert Prune.
- **Unsigned SPK:** DSM verlangt ggf. „Any Publisher“. Eine Synology-Signatur ist ohne Synology-Developer-Account nicht möglich.
- Der Build wurde mit Mock-API und lokalen Git-Repos getestet, nicht auf jeder DSM-Hardware. Die Paket-Lifecycle-Skripte sind syntaktisch geprüft; der erste echte Installationslauf sollte auf der Ziel-NAS erfolgen.
