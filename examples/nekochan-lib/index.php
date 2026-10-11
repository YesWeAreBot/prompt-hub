<?php
require __DIR__ . '/_ph/sdk.php';

require __DIR__ . '/vendor/cats-role/role.php';

$nickname = ph_required('chat.user.nickname');
$affinity = (int) ph_required('chat.user.affinity');

$lines = [
    cats_role($nickname, $affinity),
    '',
    '【本轮】',
    '- 标识：' . ph_turn(),
];

echo implode("\n", $lines), "\n";
