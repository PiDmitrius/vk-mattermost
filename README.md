# vk-mattermost

Шлюз между сообществом ВКонтакте и [OpenClaw](https://openclaw.ai).

Прикидывается сервером Mattermost: OpenClaw подключается к нему как к обычному Mattermost-каналу, а шлюз передаёт сообщения в VK и обратно через Long Poll API.

## Требования

- Go 1.21+
- Сообщество ВКонтакте с токеном бота

## Получение токена VK

1. Зайти в управление сообществом → **Настройки** → **Работа с API**
2. Создать ключ доступа, выдать права: **Сообщения**, **Управление**
3. Включить **Long Poll API**: Настройки → Long Poll API → включить, версия 5.199, событие **Входящие сообщения**

Подробнее: https://dev.vk.com/ru/api/bots/getting-started

## Установка

```bash
git clone https://github.com/PiDmitrius/vk-mattermost.git
cd vk-mattermost
make install
```

Бинарь устанавливается в `~/.local/bin/vk-mattermost` и регистрируется как systemd user service с автозапуском.

## Конфигурация

Создать файл `~/.config/vk-mattermost/config.json`:

```json
{
  "vk_token":      "vk1.a.YOUR_TOKEN_HERE",
  "allowed_users": [123456789],
  "listen":        ":8065"
}
```

- `vk_token` — токен группы из настроек сообщества
- `allowed_users` — список VK ID пользователей, которым разрешено общаться с ботом
- `listen` — адрес и порт (по умолчанию `:8065` — стандартный порт Mattermost, чтобы OpenClaw подключался без дополнительной настройки)

`chmod 600 ~/.config/vk-mattermost/config.json`

## Настройка OpenClaw

Добавить в `openclaw.json`:

```json
"channels": {
  "mattermost": {
    "enabled": true,
    "baseUrl": "http://localhost:8065",
    "botToken": "any-token",
    "allowPrivateNetwork": true,
    "allowFrom": ["vk-user-123456789"],
    "dmPolicy": "open"
  }
}
```

`allowFrom` — те же ID что в `allowed_users`, но с префиксом `vk-user-`.

`allowPrivateNetwork: true` нужен для новых версий OpenClaw, если мост работает на `localhost`, `127.0.0.1` или другом private/internal адресе. Без этого OpenClaw может блокировать подключение к Mattermost-мосту по SSRF-политике.

Если в логах OpenClaw видно что-то вроде `SsrFBlockedError` или `Blocked hostname or private/internal/special-use IP address`, проверь, что `allowPrivateNetwork` включён именно в `channels.mattermost`.

## Управление

```bash
systemctl --user status vk-mattermost
systemctl --user restart vk-mattermost
systemctl --user stop vk-mattermost
```

После изменений в коде:

```bash
make install
```
