import unittest
from types import SimpleNamespace
from unittest.mock import patch

from rag.nlp.synonym import Dealer


class SynonymOrderTests(unittest.TestCase):
    def test_wordnet_is_sorted_before_the_topn_limit(self):
        dealer = Dealer.__new__(Dealer)
        dealer.dictionary = {}
        dealer.redis = None
        dealer.lookup_num = 0
        names = ["zulu", "echo", "alpha", "delta", "beta", "gamma", "foxtrot", "hotel", "india", "sample", "alpha"]
        senses = [SimpleNamespace(name=lambda name=name: name + ".n.01") for name in names]
        with patch("rag.nlp.synonym.wordnet.synsets", return_value=senses):
            self.assertEqual(dealer.lookup("sample", topn=3), ["alpha", "beta", "delta"])
        dealer.dictionary = {"sample": ["zulu", "alpha"]}
        self.assertEqual(dealer.lookup("sample"), ["zulu", "alpha"])


if __name__ == "__main__":
    unittest.main()
