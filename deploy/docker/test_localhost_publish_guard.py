from contextlib import nullcontext
import subprocess
import unittest

import localhost_publish_guard as guard


class Firewall:
    def __init__(self):
        self.rules = [['-s', '192.0.2.0/24', '-j', 'RETURN']]
        self.calls = []
        self.fail = None

    def __call__(self, argv, **options):
        self.assert_prefix(argv)
        assert options.get('timeout') == 15
        args = argv[5:]
        self.calls.append(args)
        if self.fail == args[0]:
            return subprocess.CompletedProcess(argv, 2, '', 'synthetic command failure')
        code = 0
        output = ''
        if args[0] == '-S':
            import shlex
            output = '\n'.join('-A PREROUTING ' + shlex.join(rule) for rule in self.rules)
        elif args[0] == '-C':
            code = 0 if args[2:] in self.rules else 1
        elif args[0] == '-I':
            self.rules.insert(0, args[3:])
        elif args[0] == '-D':
            self.rules.remove(args[2:])
        else:
            raise AssertionError(args)
        return subprocess.CompletedProcess(argv, code, output, '')

    @staticmethod
    def assert_prefix(argv):
        assert argv[:5] == ['iptables', '-w', '5', '-t', 'raw']
        assert argv[5] in ('-S', '-C', '-I', '-D')


class GuardTests(unittest.TestCase):
    def test_install_is_idempotent_and_keeps_unrelated_rules(self):
        firewall = Firewall()
        unrelated = list(firewall.rules[0])
        first = guard.ensure(firewall, lock=nullcontext())
        second = guard.ensure(firewall, lock=nullcontext())
        self.assertTrue(first['changed'])
        self.assertFalse(second['changed'])
        self.assertEqual(firewall.rules, [guard.RULE, unrelated])
        self.assertEqual(sum(call[0] == '-I' for call in firewall.calls), 1)

    def test_check_does_not_mutate_policy(self):
        firewall = Firewall()
        self.assertFalse(guard.check(firewall)['present'])
        self.assertTrue(all(call[0] in ('-S', '-C') for call in firewall.calls))

    def test_created_rule_can_be_rolled_back_without_touching_other_rules(self):
        firewall = Firewall()
        original = [list(x) for x in firewall.rules]
        guard.ensure(firewall, lock=nullcontext())
        result = guard.remove_if_created(False, guard.POLICY_SHA256, firewall, lock=nullcontext())
        self.assertTrue(result['changed'])
        self.assertEqual(firewall.rules, original)
        self.assertFalse(guard.remove_if_created(False, guard.POLICY_SHA256, firewall, lock=nullcontext())['changed'])

    def test_preexisting_rule_is_preserved(self):
        firewall = Firewall();firewall.rules.insert(0, list(guard.RULE))
        self.assertFalse(guard.remove_if_created(True, guard.POLICY_SHA256, firewall, lock=nullcontext())['changed'])
        self.assertEqual(firewall.rules[0], guard.RULE)

    def test_preexisting_rule_behind_a_return_is_rejected_without_reordering(self):
        firewall = Firewall();firewall.rules.append(list(guard.RULE))
        before = [list(x) for x in firewall.rules]
        with self.assertRaises(RuntimeError):guard.ensure(firewall, lock=nullcontext())
        self.assertEqual(firewall.rules, before)

    def test_command_timeout_propagates(self):
        def hung(argv, **options):
            self.assertEqual(options['timeout'], 15)
            raise subprocess.TimeoutExpired(argv, options['timeout'])
        with self.assertRaises(subprocess.TimeoutExpired):guard.check(hung)

    def test_unexpected_or_duplicate_owned_policy_is_rejected(self):
        for rows in ([['-m', 'comment', '--comment', guard.LABEL, '-j', 'ACCEPT']],
                     [list(guard.RULE), list(guard.RULE)]):
            with self.subTest(rows=rows):
                firewall = Firewall();firewall.rules += rows
                before = [list(x) for x in firewall.rules]
                with self.assertRaises(RuntimeError):guard.ensure(firewall, lock=nullcontext())
                self.assertEqual(firewall.rules, before)

    def test_command_failure_is_not_treated_as_absent(self):
        for operation in ('-S', '-C', '-I'):
            with self.subTest(operation=operation):
                firewall = Firewall();firewall.fail = operation
                with self.assertRaises(RuntimeError):guard.ensure(firewall, lock=nullcontext())
                self.assertEqual(len(firewall.rules), 1)

    def test_rollback_requires_the_original_policy_identity(self):
        firewall = Firewall()
        for original, digest in ((None, guard.POLICY_SHA256), (False, '0' * 64)):
            with self.assertRaises(ValueError):guard.remove_if_created(original, digest, firewall, lock=nullcontext())
        with self.assertRaises(RuntimeError):guard.remove_if_created(True, guard.POLICY_SHA256, firewall, lock=nullcontext())


if __name__ == '__main__':
    unittest.main()
