---
type: backup-guide
tool: restic
topic: restore-on-another-computer
updated_at: 2026-05-31
---

# Restore From Router On Another Computer

Эта инструкция нужна, чтобы на другом компьютере восстановить Obsidian-бэкап с роутера.

Бэкап хранится на роутере в зашифрованном виде. Без пароля восстановить данные невозможно.

## Что нужно

1. Компьютер должен быть в той же сети, что и роутер.
2. Должен быть доступ к сетевому диску роутера.
3. Нужно установить `restic`.
4. Нужно знать пароль от бэкапа.

## Где лежит бэкап на роутере

На Mac он был подключен так:

```text
smb://192.168.31.1/jms583(db01)
```

Папка бэкапа:

```text
Backups/restic-memory
```

Полный сетевой путь для Windows:

```text
\\192.168.31.1\jms583(db01)\Backups\restic-memory
```

## Windows

### 1. Установить restic

Скачать `restic`:

```text
https://restic.net
```

Или установить через пакетный менеджер, если он есть.

Проверить в PowerShell:

```powershell
restic version
```

### 2. Подключить диск роутера

В PowerShell:

```powershell
net use Z: "\\192.168.31.1\jms583(db01)"
```

Если Windows попросит логин/пароль роутера, ввести их в системном окне.

Проверить:

```powershell
dir Z:\Backups\restic-memory
```

В папке должны быть:

```text
config
data
index
keys
snapshots
locks
```

### Если Windows блокирует guest-доступ

Если появляется ошибка:

```text
Вы не можете получить доступ к этой общей папке, так как политики безопасности вашей организации блокируют гостевой доступ без проверки подлинности.
```

значит Windows не разрешает подключаться к SMB-шаре как `guest` с пустым паролем.

Правильное решение:

1. Открыть админку роутера.
2. Найти раздел USB-диска / Storage / Samba / SMB.
3. Создать отдельного пользователя для бэкапов, например:

```text
login: backup
password: <новый пароль>
```

4. Дать этому пользователю доступ к диску `jms583(db01)`.
5. Подключить диск на Windows:

```powershell
net use Z: "\\192.168.31.1\jms583(db01)" /user:backup
```

Если обычный логин не сработал:

```powershell
net use Z: "\\192.168.31.1\jms583(db01)" /user:192.168.31.1\backup
```

Не использовать `root` как SMB-пользователя, если роутер не требует этого специально. Для бэкапа лучше отдельный ограниченный пользователь.

### 3. Посмотреть список бэкапов

```powershell
$env:RESTIC_REPOSITORY="Z:\Backups\restic-memory"
restic snapshots
```

`restic` спросит пароль от бэкапа.

### 4. Восстановить последний бэкап

```powershell
$env:RESTIC_REPOSITORY="Z:\Backups\restic-memory"
restic restore latest --target "$env:USERPROFILE\Desktop\memory-restore"
```

После восстановления открыть:

```text
Desktop\memory-restore
```

Внутри будет восстановленная структура с Obsidian vault.

## Другой Mac

### 1. Подключить роутер

Finder:

```text
Go -> Connect to Server
```

Адрес:

```text
smb://192.168.31.1
```

Выбрать диск:

```text
jms583(db01)
```

### 2. Установить restic

Если есть Homebrew:

```bash
brew install restic
```

Проверить:

```bash
restic version
```

### 3. Найти путь к диску

```bash
ls /Volumes
```

Обычно путь будет похож на:

```text
/Volumes/jms583(db01)
```

### 4. Посмотреть список бэкапов

```bash
restic -r "/Volumes/jms583(db01)/Backups/restic-memory" snapshots
```

### 5. Восстановить последний бэкап

```bash
restic -r "/Volumes/jms583(db01)/Backups/restic-memory" restore latest --target ~/Desktop/memory-restore
```

После восстановления открыть:

```text
~/Desktop/memory-restore
```

## Что написать Codex после восстановления

Когда файлы восстановились, напиши мне:

```text
Я восстановил бэкап в папку: <путь к папке>
Помоги открыть Obsidian vault и проверить, что всё на месте.
```

Например:

```text
Я восстановил бэкап в папку: C:\Users\Dmitriy\Desktop\memory-restore
Помоги открыть Obsidian vault и проверить, что всё на месте.
```

## Важно

- Не редактировать руками папку `restic-memory`.
- Не удалять `config`, `data`, `index`, `keys`, `snapshots`.
- Не терять пароль от бэкапа.
- Восстановление лучше делать в новую папку, а не поверх существующего Obsidian.
