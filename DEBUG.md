# Отладка Grim Coin

## Включена расширенная отладка

Текущая версия включает подробные логи для диагностики проблем с доставкой сообщений.

### Отладочные логи

#### ACK отправка и получение
```
[ACK] Sent TRANSACTION ACK for message 1759576757921735 to 10.92.28.200
[ACK] Received ACK for message 1759576757921735 (registered: true)
```

#### Транзакции
```
Target nodes for transaction: [10.92.28.200 10.92.28.89]
Broadcasting transaction to 2 nodes: [10.92.28.200 10.92.28.89]
```

## Диагностика проблем с ACK

### Проблема: Транзакция не получает ACK

**Что проверить:**

1. **Проверьте таблицу перед транзакцией:**
```bash
> table

=== Network Table ===
Version: 3
Wallets:
  CONTROLLER (10.92.28.89): 0 coins
  Gleb (10.92.28.191): 100 coins (YOU)
  Ivan (10.92.28.200): 100 coins
```

2. **Убедитесь что IP адреса актуальные:**
   - IP в таблице должны совпадать с реальными IP узлов
   - Если узел перезапускался с другим IP, таблица может быть неактуальной

3. **Проверьте логи получателя:**
   
   Получатель должен показать:
   ```
   Received transaction: Gleb -> Ivan: 10 coins (TX: c4625f17)
   [ACK] Sent TRANSACTION ACK for message 1759576757921735 to 10.92.28.191
   ```

   Если ACK отправлен, но не получен отправителем - проблема в сети.

4. **Проверьте логи отправителя:**
   
   Отправитель должен показать:
   ```
   Broadcasting transaction to 2 nodes: [10.92.28.200 10.92.28.89]
   [ACK] Received ACK for message 1759576757921735 (registered: true)
   [ACK] Received ACK for message 1759576757921735 (registered: true)
   ```

   Если "(registered: false)" - сообщение пришло, но канал уже закрыт.

### Типичные проблемы

#### 1. IP адреса не совпадают

**Симптомы:**
```
Target nodes for transaction: [10.92.28.200 10.92.28.89]
Retry 1/5 for message ... to 10.92.28.89: failed to deliver
```

**Причина:**
IP в таблице (10.92.28.89) не совпадает с реальным IP узла.

**Решение:**
1. Перезапустите все узлы
2. Убедитесь что все выбрали правильный сетевой интерфейс
3. Проверьте `table` на всех узлах

#### 2. Firewall блокирует UDP

**Симптомы:**
ACK отправляется, но не принимается.

**Решение:**
```bash
# Linux
sudo ufw allow 9999/udp
sudo iptables -I INPUT -p udp --dport 9999 -j ACCEPT

# Проверить
sudo tcpdump -i any udp port 9999 -n
```

#### 3. Разные подсети

**Симптомы:**
HELLO не доходит между узлами.

**Решение:**
Убедитесь что все узлы в одной подсети:
```bash
ip addr show
# Все должны быть в 10.92.28.x/24 или 192.168.1.x/24
```

### Тестовый сценарий

```bash
# Терминал 1 (Контроллер)
make run-controller
# Выберите интерфейс с IP 10.92.28.89

# Терминал 2 (Кошелек Alice)
make run-wallet
# Выберите интерфейс с IP 10.92.28.191
# Введите ID: Alice

# Терминал 3 (Кошелек Bob)  
make run-wallet
# Выберите интерфейс с IP 10.92.28.200
# Введите ID: Bob

# В Alice проверить таблицу
> table
# Должны видеть:
# CONTROLLER (10.92.28.89)
# Alice (10.92.28.191)
# Bob (10.92.28.200)

# Отправить транзакцию
> send Bob 10

# Ожидаемый вывод:
Target nodes for transaction: [10.92.28.89 10.92.28.191 10.92.28.200]
Acquiring distributed lock...
Sending lock requests to 2 nodes...
...
Lock acquired, processing transaction...
Broadcasting transaction to 3 nodes: [10.92.28.89 10.92.28.191 10.92.28.200]
[ACK] Received ACK for message ... (registered: true)
[ACK] Received ACK for message ... (registered: true)
[ACK] Received ACK for message ... (registered: true)
Transaction broadcasted, syncing table...
[ACK] Received ACK for message ... (registered: true)
[ACK] Received ACK for message ... (registered: true)
[ACK] Received ACK for message ... (registered: true)
Transaction completed successfully!
```

### Если ACK не приходит

**Шаг 1: Проверьте отправку на получателе**

В логах Bob должно быть:
```
Received transaction: Alice -> Bob: 10 coins (TX: xxx)
[ACK] Sent TRANSACTION ACK for message ... to 10.92.28.191
```

Если нет "[ACK] Sent" - получатель не получил транзакцию вообще.

**Шаг 2: Проверьте получение на отправителе**

В логах Alice должно быть:
```
[ACK] Received ACK for message ... (registered: true)
```

Если нет - ACK не дошел. Проверьте сеть:
```bash
# На машине Alice
sudo tcpdump -i any udp port 9999 -n | grep 10.92.28.200
```

**Шаг 3: Проверьте IP маршрутизацию**

```bash
# На машине Alice
ping 10.92.28.200  # Должен отвечать

# Проверить UDP
nc -u 10.92.28.200 9999  # Ввести текст, должен дойти
```

### Packet Capture для глубокой отладки

```bash
# На отправителе (Alice)
sudo tcpdump -i any udp port 9999 -w /tmp/alice.pcap

# На получателе (Bob)
sudo tcpdump -i any udp port 9999 -w /tmp/bob.pcap

# Сделать транзакцию

# Проанализировать
wireshark /tmp/alice.pcap
wireshark /tmp/bob.pcap
```

Ищите:
1. Пакет TRANSACTION от Alice к Bob
2. Пакет TRANSACTION_ACK от Bob к Alice

Если (1) есть, но (2) нет - проблема в коде получателя.
Если оба есть, но Alice не видит ACK - проблема в коде отправителя.

### Быстрый фикс: Проверка IP

Добавьте в начало транзакции проверку IP:

```bash
# Перед send Bob 10
> table

# Убедитесь что IP Bob правильный
# Если Bob показывает другой IP при запуске - перезапустите все узлы
```

## Отключение отладочных логов

После решения проблемы можно закомментировать debug логи:

В `cmd/wallet/main.go` и `cmd/controller/main.go`:

```go
// fmt.Printf("[ACK] Sent %s ACK for message %s to %s\n", ...)
// fmt.Printf("[ACK] Received ACK for message %s (registered: %v)\n", ...)
```

Затем:
```bash
make build
```

## Контакты

Если проблема не решается - соберите полные логи:
- Контроллер
- Все кошельки
- Вывод `table` со всех узлов
- Вывод `ip addr show` со всех машин

И опишите проблему с этими данными.
