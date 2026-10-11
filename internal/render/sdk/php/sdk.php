<?php

declare(strict_types=1);

$__ph_raw = stream_get_contents(STDIN);
$__ph_env = json_decode($__ph_raw === false ? '' : $__ph_raw, true);
if (!is_array($__ph_env)) {
    fwrite(STDERR, "prompt-hub SDK: 无法解析 stdin 的 envelope\n");
    exit(2);
}

function ph(string $key, ?string $default = null): ?string
{
    global $__ph_env;
    $v = $__ph_env['context'][$key] ?? null;
    if ($v === null) {
        return $default;
    }
    return is_scalar($v) ? (string) $v : json_encode($v, JSON_UNESCAPED_UNICODE);
}

function ph_required(string $key): string
{
    $v = ph($key);
    if ($v === null || $v === '') {
        fwrite(STDERR, "缺少必需 context 字段: {$key}\n");
        exit(3);
    }
    return $v;
}

function ph_now(): string
{
    global $__ph_env;
    return (string) ($__ph_env['now'] ?? '');
}

function ph_turn(): string
{
    global $__ph_env;
    return (string) ($__ph_env['turn'] ?? '');
}

function ph_pack(): string
{
    global $__ph_env;
    return ($__ph_env['packId'] ?? '?') . '@' . ($__ph_env['version'] ?? '?');
}

function ph_output_max(): int
{
    global $__ph_env;
    return (int) ($__ph_env['outputMax'] ?? 0);
}

function ph_view(): string
{
    global $__ph_env;
    return (string) ($__ph_env['viewDir'] ?? '');
}

function ph_env(): array
{
    global $__ph_env;
    return $__ph_env;
}
