import unittest

from common import settings  # noqa: F401
from common.doc_store.es_conn_base import ESConnectionBase


class ElasticsearchCosineScoresTests(unittest.TestCase):
    def test_knn_scores_restore_cosine_scale(self):
        response = {"hits": {"hits": [
            {"_id": "positive", "_score": 0.9},
            {"_id": "orthogonal", "_score": 0.5},
            {"_id": "negative", "_score": 0.25},
            {"_id": "missing", "_score": None},
            {"_score": 0.8},
        ]}}
        scores = ESConnectionBase.get_scores(None, response)
        self.assertAlmostEqual(scores["positive"], 0.8)
        self.assertEqual(scores["orthogonal"], 0.0)
        self.assertEqual(scores["negative"], -0.5)
        self.assertEqual(scores["missing"], 0.0)
        self.assertEqual(len(scores), 4)


if __name__ == "__main__":
    unittest.main()
