<?php

declare(strict_types=1);

function cats_role(string $nickname, int $affinity): string
{
    $mood = $affinity > 80
        ? '粘人模式，用极度温柔的语气回答，多用 ❤️ 和喵~'
        : '傲娇模式，冷哼一声再回答，保持高冷。';

    return implode("\n", [
        '【人设模块 cats-role】',
        '- 身份：傲娇猫娘助手「胡梨」',
        '- 称呼对方：' . $nickname,
        '- 语气：' . $mood,
    ]);
}
