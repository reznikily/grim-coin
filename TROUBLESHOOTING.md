# Решение проблем Grim Coin

## 🐛 Известные проблемы и решения

### Проблема 1: "failed to acquire lock" 

**Симптомы:**
```
Lock granted to Alice (request: 12ab34cd)
failed to acquire lock from all nodes
```

**Причина:**
- `LOCK_GRANT` не доставлялся из-за потери UDP пакетов
- Недостаточно повторов при отправке

**Решение (исправлено в текущей версии):**
- `LOCK_REQUEST` теперь отправляется каждому узлу индивидуально (не через broadcast)
- Каждый запрос отправляется **3 раза** с интервалом 100ms
- `LOCK_GRANT` отправляется **5 раз** с интервалом 200ms
- Таймаут увеличен до 10 секунд

### Проблема 2: Дублирование транзакций (ИСПРАВЛЕНО ✅)

**Симптомы:**
```
> send Bob 10
Transaction completed successfully!

# В логах контроллера:
Received transaction: Alice -> Bob: 10 coins (TX: xxx)
Received transaction: Alice -> Bob: 10 coins (TX: xxx)
Received transaction: Alice -> Bob: 10 coins (TX: xxx)
...8 раз

# Итог: вместо 10 монет переводится 80
```

**Причина:**
1. Отсутствие дедупликации транзакций по TX ID
2. ACK не отправляется, поэтому отправитель делает 5 повторов
3. Каждый повтор применяется, так как нет проверки на дубликаты

**Решение (исправлено):**
- Добавлена **дедупликация транзакций** по TX ID
- Каждый узел (кошелек и контроллер) хранит `map[string]bool` обработанных транзакций
- При получении дубликата транзакция пропускается с логом:
  ```
  Skipping duplicate transaction: 22829808
  ```

**Код исправления:**
```go
// В Wallet и Controller добавлено:
processedTxs    map[string]bool
processedTxsMu  sync.RWMutex

// В handleTransaction:
c.processedTxsMu.Lock()
if c.processedTxs[txData.TxID] {
    c.processedTxsMu.Unlock()
    fmt.Printf("Skipping duplicate transaction: %s\n", txData.TxID[:8])
    return nil
}
c.processedTxs[txData.TxID] = true
c.processedTxsMu.Unlock()
```

### Проблема 3: Кошелек не видит контроллер

**Симптомы:**
- Кошелек запускается, но не получает таблицу
- В логах кошелька нет "Received HELLO from CONTROLLER"

**Возможные причины:**
1. Контроллер и кошелек в разных подсетях
2. Firewall блокирует UDP порт 9999
3. Broadcast не работает в сети

**Решение:**
```bash
# 1. Проверьте, что оба в одной подсети
ip addr show

# 2. Проверьте firewall (Linux)
sudo iptables -L | grep 9999
sudo ufw status

# Разрешите порт 9999 UDP
sudo ufw allow 9999/udp

# 3. Проверьте broadcast адрес
# Для 192.168.1.x broadcast должен быть 192.168.1.255
```

### Проблема 4: Dashboard не подключается

**Симптомы:**
- Dashboard показывает "🔴 Disconnected"
- В консоли браузера: "WebSocket connection failed"

**Решение:**
1. Убедитесь, что контроллер запущен
2. Проверьте URL в логах контроллера:
   ```
   WebSocket server: ws://192.168.1.100:8080/ws
   Dashboard: http://192.168.1.100:8081/dashboard.html
   ```
3. Откройте именно этот URL
4. Проверьте firewall для портов 8080 и 8081:
   ```bash
   sudo ufw allow 8080/tcp
   sudo ufw allow 8081/tcp
   ```

### Проблема 5: Транзакции не проходят

**Симптомы:**
```
Transaction failed: insufficient balance
```

**Решение:**
```bash
# Проверьте баланс
> balance

# Проверьте таблицу сети
> table

# Убедитесь что:
# 1. У вас достаточно монет
# 2. Получатель существует в таблице
# 3. ID получателя написан правильно (с учетом регистра)
```

### Проблема 6: "Lock denied" при попытке транзакции

**Симптомы:**
```
Lock denied to Alice (held by Bob)
failed to acquire lock from all nodes
```

**Причина:**
Другой узел уже совершает транзакцию

**Решение:**
Подождите завершения текущей транзакции (обычно 3-5 секунд) и попробуйте снова

### Проблема 7: Множественные "Warning: received grant for unknown request"

**Симптомы:**
```
Warning: received grant for unknown request 670d6e1d
Warning: received grant for unknown request 670d6e1d
Warning: received grant for unknown request 670d6e1d
```

**Причина:**
`LOCK_GRANT` продолжает приходить после закрытия канала (это нормально из-за множественных повторов)

**Это нормально:**
Это не ошибка - просто предупреждение. `LOCK_GRANT` отправляется 5 раз для надежности, и некоторые приходят после того, как блокировка уже получена.

## 🔍 Отладка

### Включить подробное логирование

Добавьте отладочные принты в код:

```go
// В handleTransaction
fmt.Printf("[DEBUG] Processing TX: %s, From: %s, To: %s, Amount: %d\n", 
    txData.TxID[:8], txData.From, txData.To, txData.Amount)

// В acquireDistributedLock
fmt.Printf("[DEBUG] Waiting for %d nodes, got %d grants\n", len(nodeIPs), granted)
```

### Проверить сетевую связность

```bash
# Проверить, что UDP работает
nc -u 192.168.1.100 9999

# Сниффинг UDP трафика
sudo tcpdump -i any udp port 9999 -v
```

### Проверить состояние кошелька

```bash
# В кошельке:
> table

# Должно показать всех участников сети:
=== Network Table ===
Version: 5
Wallets:
  CONTROLLER (192.168.1.100): 0 coins
  Alice (192.168.1.101): 75 coins (YOU)
  Bob (192.168.1.102): 125 coins
```

## 🛠️ Сброс состояния

### Полный сброс кошелька

```bash
# Удалить профиль
rm .wallet_profile.json

# Пересоздать с новым ID
make run-wallet
```

### Перезапуск сети

```bash
# 1. Остановить все кошельки (Ctrl+C)
# 2. Остановить контроллер (Ctrl+C)
# 3. Запустить контроллер
make run-controller

# 4. Запустить кошельки
make run-wallet
```

## 📊 Проверка версий

### Проверить что используете последнюю версию

```bash
# Пересобрать проект
make clean
make build

# Проверить размер бинарников
ls -lh bin/

# Должно быть примерно:
# controller: ~7.8M
# wallet: ~3.2M
```

## 🔐 Безопасность

### Проблемы безопасности текущей версии

⚠️ **Внимание:** Это учебный проект. В production не используйте без:

1. **Аутентификации** - любой может отправить любое сообщение
2. **Шифрования** - все данные в открытом виде
3. **Подписей** - транзакции не подписаны криптографически
4. **Защиты от replay** - старые транзакции можно повторить
5. **Консенсуса** - нет защиты от злонамеренных узлов

## 📞 Поддержка

Если проблема не решена:

1. Проверьте логи всех узлов (контроллер + все кошельки)
2. Убедитесь что используете последнюю версию кода
3. Проверьте сетевые настройки (firewall, подсеть)
4. Попробуйте с двумя узлами сначала (контроллер + 1 кошелек)

## 🧪 Тестовый сценарий

Чтобы убедиться что все работает:

```bash
# Терминал 1
make run-controller
# Выберите интерфейс

# Терминал 2
make run-wallet
# Выберите интерфейс, введите ID "Alice"

# Терминал 3
make run-wallet  
# Выберите интерфейс, введите ID "Bob"

# В Alice:
> table
# Должны видеть Alice, Bob, CONTROLLER

> balance
# Должно быть 100 coins

> send Bob 25

# Ждем "Transaction completed successfully!"

> balance
# Должно быть 75 coins

# В Bob:
> balance  
# Должно быть 125 coins

# В браузере открыть dashboard
# Должны видеть Alice: 75, Bob: 125
```

Если этот сценарий проходит - все работает правильно! ✅
