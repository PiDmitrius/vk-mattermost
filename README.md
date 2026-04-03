# vk-mattermost

Единый шлюз VK + MAX -> OpenClaw через fake Mattermost API.

OpenClaw подключается к мосту как к обычному Mattermost-каналу. Мост пересылает сообщения в VK и/или MAX и обратно через Long Poll.

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

## Конфигурация

`~/.config/vk-mattermost/config.json`:

```json
{
  "vk_token":           "vk1.a.YOUR_TOKEN",
  "allowed_users":      [123456789],
  "max_token":          "YOUR_MAX_BOT_TOKEN",
  "max_allowed_users":  [12345678],
  "listen":             ":8065"
}
```

Все поля опциональны, кроме хотя бы одного из `vk_token` / `max_token`.

- `vk_token` -- токен сообщества VK ([Настройки -> API -> Ключ доступа](https://dev.vk.com/ru/api/bots/getting-started))
- `allowed_users` -- VK ID пользователей, которым разрешено писать
- `max_token` -- токен бота MAX ([business.max.ru -> Чат-боты -> Интеграция -> Получить токен](https://business.max.ru/self))
- `max_allowed_users` -- MAX user ID, которым разрешено писать в DM (групповые чаты фильтруются через OpenClaw allowFrom)
- `listen` -- адрес привязки (по умолчанию `:8065`)

`chmod 600 ~/.config/vk-mattermost/config.json`

## Настройка VK

Документация: https://dev.vk.com/ru/api/bots/getting-started

1. Настройки сообщества -> API -> создать ключ доступа (Сообщения + Управление)
2. Включить Long Poll API: версия 5.199, событие "Входящие сообщения"
3. Напишите боту `/i` -- бот ответит вашим ID:
   ```
   `123456789` 🦞
   ```
   Добавьте в `allowed_users`, перезапустите мост.

## Настройка MAX

Документация: https://dev.max.ru/docs/chatbots\
Платформа: https://business.max.ru/self

1. Создать бота: business.max.ru -> Чат-боты -> Интеграция -> Получить токен
2. Напишите боту `/i` в DM -- бот ответит вашим ID:
   ```
   `12345678` 🦞
   ```
   Добавьте в `max_allowed_users`, перезапустите мост.
3. **Групповые чаты**: добавьте бота в группу и **назначьте админом** (обязательно для получения сообщений).
4. Напишите `/i` в группу -- бот ответит:
   ```
   `12345678` @ `-100000000000` 🦞
   ```
   Второе число -- chat_id. Добавьте `max-chat-{chat_id}` в `allowFrom` OpenClaw (см. ниже).

## Маппинг ID

| Источник      | user_id в OpenClaw       | channel_id в OpenClaw      |
|---------------|--------------------------|----------------------------|
| VK DM         | `vk-user-{vk_id}`       | `vk-dm-{vk_id}`           |
| MAX DM        | `max-user-{user_id}`    | `max-dm-{user_id}`        |
| MAX группа    | `max-chat-{chat_id}`    | `max-chat-{chat_id}`      |

## Настройка OpenClaw

Добавить в `openclaw.json`:

```json
"channels": {
  "mattermost": {
    "enabled": true,
    "baseUrl": "http://localhost:8065",
    "botToken": "any-token",         // мост не проверяет, любое значение
    "allowPrivateNetwork": true,
    "dmPolicy": "open",
    "allowFrom": [
      "vk-user-123456789",
      "max-user-12345678",
      "max-chat--100000000000"
    ]
  }
}
```

- `allowFrom` управляет кому OpenClaw отвечает
- VK DM: `vk-user-{id}`
- MAX DM: `max-user-{id}`
- MAX группа: `max-chat-{chat_id}` (отрицательный, обратите внимание на двойной дефис)
- `allowPrivateNetwork: true` обязателен для localhost-моста (иначе SSRF-блокировка)

## Управление

```bash
systemctl --user status vk-mattermost
systemctl --user restart vk-mattermost
make install  # пересборка + перезапуск
```
