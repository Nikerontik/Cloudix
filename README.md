# Cloudix

Локальный P2P-мессенджер без центрального сервера. Работает **только по локальной сети**
(Wi-Fi/Ethernet, в том числе виртуальная LAN через Radmin VPN) — режим Bluetooth убран,
чтобы не тащить в проект нестабильные нативные BLE-мосты и не плодить лишние баги на хакатоне.

Бэкенд — Go (GoLand), P2P — libp2p + mDNS-дискавери в локальной сети, локальное хранилище —
SQLite (без единого сервера). UI — React + Vite + framer-motion, обёрнут в Wails v2 (нативное
окно на Go, без Electron).

## Структура проекта

```
cloudix_messenger/
├── main.go                  # точка входа Wails, заголовок окна "Cloudix"
├── go.mod                   # module cloudix
├── wails.json
├── backend/
│   ├── app/app.go           # ConnectTransport (только LAN), SendMessage, SaveProfile
│   ├── models/models.go     # TransportMode ограничен одним значением: lan_wifi
│   ├── p2p/p2p.go           # NewLANNode — единственная реализация транспорта
│   └── storage/storage.go   # локальная SQLite база
└── frontend/
    ├── src/App.jsx          # UI: чаты, группы/каналы/избранное, настройки без выбора Bluetooth
    ├── src/styles/theme.css # liquid glass стиль, светлая/тёмная тема, анимации
    └── ...
```

## Что изменилось по сравнению с прошлой версией

- Убрана заглушка `NewBluetoothNode` и весь связанный код — из `p2p.go`, `app.go`, `models.go`
  и настроек фронтенда. Bluetooth больше не упоминается в UI.
- `TransportMode` теперь содержит только `lan_wifi`, `ConnectTransport` возвращает ошибку на
  любое другое значение.
- Приложение переименовано в **Cloudix** (заголовок окна, go.mod module, package.json, wails.json).
- Фронтенд теперь автоматически поднимает LAN-соединение при старте приложения (`useEffect` в
  `App.jsx`), пользователю не нужно вручную выбирать транспорт — он один.

## Что ещё нужно доделать (заглушки)

- Реальная рассылка сообщений через libp2p streams между участниками чата — сейчас сообщение
  только пишется в локальную SQLite, TODO помечен в `backend/app/app.go`.
- Иконка приложения (.ico для Windows, .icns для macOS) — сейчас используется дефолтная иконка
  Wails, нужно положить свою в `build/appicon.png` перед сборкой.
- Полноценный splash-экран при первом запуске (сейчас есть только индикатор соединения в шапке чата).

---

## План действий: запуск на macOS

1. **Установить Homebrew** (если ещё нет): `https://brew.sh`
2. **Установить Go 1.22+**:
   ```
   brew install go
   go version
   ```
3. **Установить Node.js 18+**:
   ```
   brew install node
   node -v
   ```
4. **Установить Wails CLI**:
   ```
   go install github.com/wailsapp/wails/v2/cmd/wails@latest
   ```
   Добавить `$HOME/go/bin` в `PATH`, если команда `wails` не находится:
   ```
   export PATH=$PATH:$HOME/go/bin
   ```
5. **Проверить окружение**:
   ```
   wails doctor
   ```
   На macOS обычно нужен Xcode Command Line Tools:
   ```
   xcode-select --install
   ```
6. **Открыть проект в GoLand**: File → Open → выбрать папку `cloudix_messenger`. GoLand сам
   подхватит `go.mod` (module cloudix) и предложит установить зависимости.
7. **Установить зависимости фронтенда** (один раз):
   ```
   cd cloudix_messenger/frontend
   npm install
   cd ..
   ```
8. **Запустить в режиме разработки** (для проверки, что всё работает):
   ```
   wails dev
   ```
   Откроется окно Cloudix, LAN-соединение поднимется автоматически.
9. **Собрать финальный .app**:
   ```
   wails build -platform darwin/universal
   ```
   Готовый файл: `build/bin/Cloudix.app`.
10. **Запустить собранный .app**: дважды кликнуть в Finder. Если macOS блокирует запуск
    (Gatekeeper, т.к. приложение не подписано) — кликнуть правой кнопкой → "Открыть" → подтвердить.

---

## План действий: запуск на Windows

1. **Установить Go 1.22+**: скачать с `https://go.dev/dl/`, выбрать `.msi` для Windows,
   установить с настройками по умолчанию.
2. **Установить Node.js 18+**: скачать LTS-версию с `https://nodejs.org`, установить.
3. **Установить Git** (если ещё нет, нужен для скачивания зависимостей): `https://git-scm.com`
4. **Установить Wails CLI** (в PowerShell или CMD):
   ```
   go install github.com/wailsapp/wails/v2/cmd/wails@latest
   ```
   Убедиться, что `%USERPROFILE%\go\bin` добавлен в переменную `PATH` (обычно Go-инсталлятор
   делает это сам, иначе — добавить вручную через "Изменить переменные среды").
5. **Проверить окружение**:
   ```
   wails doctor
   ```
   Он подскажет, если не хватает **WebView2 Runtime** — на Windows 10/11 обычно уже установлен,
   если нет — скачать с сайта Microsoft или собрать с флагом `-webview2 embed` (см. шаг 8).
6. **Открыть проект в GoLand**: File → Open → выбрать папку `cloudix_messenger`.
7. **Установить зависимости фронтенда**:
   ```
   cd cloudix_messenger\frontend
   npm install
   cd ..
   ```
8. **Запустить в режиме разработки**:
   ```
   wails dev
   ```
9. **Собрать финальный .exe**:
   ```
   wails build -platform windows/amd64 -webview2 embed
   ```
   Флаг `-webview2 embed` встраивает WebView2 runtime в сборку, чтобы не требовать отдельной
   установки у пользователя. Готовый файл: `build\bin\Cloudix.exe`.
10. **Запустить**: дважды кликнуть `Cloudix.exe`. Если Windows SmartScreen предупреждает о
    неизвестном издателе — "Подробнее" → "Выполнить в любом случае" (это нормально для
    несертифицированных .exe).

---

## Важно для тестирования LAN-режима

Чтобы два экземпляра Cloudix увидели друг друга через mDNS, оба устройства должны быть в одной
локальной сети (один Wi-Fi роутер, либо оба подключены к одной сессии Radmin VPN). Если firewall
Windows блокирует mDNS-трафик — разрешить приложению `Cloudix.exe` доступ в "Частные сети" в
настройках брандмауэра при первом запросе.
