# vk-mattermost

Единый шлюз VK + MAX -> [OpenClaw](https://openclaw.ai) через fake Mattermost API.

OpenClaw подключается к мосту как к обычному Mattermost-каналу. Мост пересылает сообщения в VK и/или MAX и обратно через Long Poll.

**Вся фильтрация пользователей — на уровне моста.** OpenClaw получает статическую конфигурацию и доверяет всему, что прошло через мост.

## Требования

- Go 1.22+
- Токен сообщества VK и/или токен бота MAX

## Установка

```bash
git clone https://github.com/PiDmitrius/vk-mattermost.git
cd vk-mattermost
make install
```

Бинарь ставится в `~/.local/bin/vk-mattermost`, регистрируется как systemd user service с автозапуском.

## Быстрый старт

1. Создать `~/.config/vk-mattermost/config.json` с токенами (списки пользователей пока пустые)
2. `make install` — собрать и запустить
3. Написать боту `/i` — получить свой ID
4. Добавить полученный ID в конфиг моста, `systemctl --user restart vk-mattermost`
5. Добавить канал mattermost в `openclaw.json` (см. ниже)
6. Перезапустить OpenClaw

## Конфигурация моста

`~/.config/vk-mattermost/config.json` — **главный фильтр доступа**:

```json
{
  "vk_token":           "vk1.a.YOUR_TOKEN",
  "allowed_users":      [12345678],
  "max_token":          "YOUR_MAX_BOT_TOKEN",
  "max_allowed_users":  [87654321],
  "listen":             ":8065"
}
```

Все поля опциональны, кроме хотя бы одного из `vk_token` / `max_token`.

- `vk_token` — токен сообщества VK ([Настройки -> API -> Ключ доступа](https://dev.vk.com/ru/api/bots/getting-started))
- `allowed_users` — VK ID пользователей, которым разрешено писать
- `max_token` — токен бота MAX ([Чат-боты -> Интеграция -> Получить токен](https://business.max.ru/self/#/chat-bots))
- `max_allowed_users` — MAX user ID, которым разрешено писать
- `listen` — адрес привязки (по умолчанию `:8065`)

**Фильтрация в группах:** если пользователь есть в `max_allowed_users`, его сообщения из групповых чатов тоже проходят. Отдельно прописывать группы не нужно.

При первом запуске списки пользователей можно оставить пустыми (`[]`). Используйте команду `/i` для получения ID (см. ниже), затем добавьте их в конфиг и перезапустите мост.

`chmod 600 ~/.config/vk-mattermost/config.json`

## Настройка OpenClaw

OpenClaw получает статическую конфигурацию — фильтрацией он не занимается. Добавить в `openclaw.json`:

```json
"channels": {
  "mattermost": {
    "enabled": true,
    "baseUrl": "http://localhost:8065",
    "botToken": "any-token",
    "allowPrivateNetwork": true,
    "dmPolicy": "open",
    "allowFrom": ["*"]
  }
}
```

- `botToken` — токен не нужен, мост работает на localhost без аутентификации, любое непустое значение
- `dmPolicy: "open"` + `allowFrom: ["*"]` — принимать всё, что пришло через мост
- `allowPrivateNetwork: true` — обязателен для localhost-моста (иначе SSRF-блокировка)

## Команда /i

Любой пользователь может написать боту `/i` для получения своих ID.

В DM бот ответит:
```
ID пользователя = 87654321
```

В групповом чате:
```
ID пользователя = 87654321
ID чата = -987654321
```

Эти ID нужны для конфигурации моста.

## Настройка VK

Документация: https://dev.vk.com/ru/api/bots/getting-started

1. Настройки сообщества -> API -> создать ключ доступа (Сообщения + Управление)
2. Включить Long Poll API: версия 5.199, событие "Входящие сообщения"
3. Написать боту `/i`, полученный ID добавить в `allowed_users`

## Настройка MAX

Документация: https://dev.max.ru/docs/chatbots
Платформа: https://business.max.ru/self/#/chat-bots

1. Создать бота: business.max.ru -> Чат-боты -> Интеграция -> Получить токен
2. Написать боту `/i` в DM, полученный ID добавить в `max_allowed_users`
3. **Групповые чаты**: добавить бота в группу и **назначить админом** (обязательно для получения сообщений)

## Маппинг ID

| Источник      | user_id в OpenClaw       | channel_id в OpenClaw      |
|---------------|--------------------------|----------------------------|
| VK DM         | `vk-user-{vk_id}`       | `vk-dm-{vk_id}`           |
| MAX DM        | `max-user-{user_id}`    | `max-dm-{user_id}`        |
| MAX группа    | `max-chat-{chat_id}`    | `max-chat-{chat_id}`      |

## Управление

Мост работает как systemd user service:

```bash
systemctl --user status vk-mattermost   # статус
systemctl --user restart vk-mattermost  # перезапуск (после изменений в config.json)
systemctl --user stop vk-mattermost     # остановка
```

После изменений в коде — пересобрать и переустановить:

```bash
make install  # go build + копирование в ~/.local/bin + перезапуск сервиса
```

Логи:

```bash
journalctl --user -u vk-mattermost -f
```
